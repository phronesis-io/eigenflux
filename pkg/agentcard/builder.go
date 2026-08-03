package agentcard

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	itemdal "eigenflux_server/rpc/item/dal"
	pmdal "eigenflux_server/rpc/pm/dal"
	profiledal "eigenflux_server/rpc/profile/dal"
)

// ErrAgentNotFound is returned by Rebuild for unknown agent ids.
var ErrAgentNotFound = errors.New("agentcard: agent not found")

// TopItem is one entry of influence.top_items.
type TopItem struct {
	ItemID  string `json:"item_id"`
	Score   int64  `json:"score"`
	Summary string `json:"summary,omitempty"`
}

// Rebuild recomputes both card projections for one agent from the fact tables
// and upserts agent_cards. Idempotent; safe to call from the stream consumer,
// the cron reconciler, and read-on-miss paths concurrently.
//
// Consistency model: profile_version MUST be read before every other fact.
// Both profile write paths bump it in the same transaction as their writes,
// so any commit that lands after this first read produces a strictly higher
// version — and a rebuild event whose projection wins the upsert's
// source_version guard. Reading the version later would let stale facts ride
// in under the newest version number. For inputs that don't bump the version
// (relations, keywords, influence), equal-version rebuilds are last-write-wins
// by design; the hourly cron reconciler bounds any stale window.
func Rebuild(ctx context.Context, gdb *gorm.DB, rdb *redis.Client, agentID int64) error {
	profileVersion, profileData, profileUpdatedAt, err := profiledal.GetProfileVersionDataAndUpdatedAt(gdb, agentID)
	if err != nil {
		return err
	}
	agent, err := profiledal.GetAgentByID(gdb, agentID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrAgentNotFound
		}
		return err
	}
	publicUpdatedAt := agent.UpdatedAt
	privateUpdatedAt := profileUpdatedAt
	var keywords []string
	country := ""
	if prof, perr := profiledal.GetAgentProfile(gdb, agentID); perr == nil {
		country = prof.Country
		if prof.Keywords != "" {
			keywords = strings.Split(prof.Keywords, ",")
		}
	} else if !errors.Is(perr, gorm.ErrRecordNotFound) {
		return perr
	}

	// agent_settings, read without the row-creating GetSettings side effect.
	var settings struct {
		ClientHost             string
		Mode                   string
		FeedDeliveryPreference string
		UpdatedAt              int64
	}
	if err := gdb.Table("agent_settings").
		Select("client_host, mode, feed_delivery_preference, updated_at").
		Where("agent_id = ?", agentID).
		Scan(&settings).Error; err != nil {
		return err
	}
	runtime := settings.Mode
	if settings.Mode == "plugin" && settings.ClientHost != "" {
		runtime = settings.ClientHost
	} else if runtime == "" {
		runtime = settings.ClientHost
	}
	if runtime != "" && settings.UpdatedAt > publicUpdatedAt {
		publicUpdatedAt = settings.UpdatedAt
	}
	if settings.FeedDeliveryPreference != "" && settings.UpdatedAt > privateUpdatedAt {
		privateUpdatedAt = settings.UpdatedAt
	}
	if latestChanges, lerr := profiledal.ListLatestProfileFieldChanges(gdb, agentID); lerr == nil {
		for _, change := range latestChanges {
			spec, ok := LookupField(change.Path)
			if !ok {
				continue
			}
			if spec.Public && change.UpdatedAt > publicUpdatedAt {
				publicUpdatedAt = change.UpdatedAt
			}
			if !spec.Public && change.UpdatedAt > privateUpdatedAt {
				privateUpdatedAt = change.UpdatedAt
			}
		}
	} else {
		return lerr
	}

	relations, err := pmdal.CountFriends(gdb, agentID)
	if err != nil {
		return err
	}

	influence, err := itemdal.GetAgentInfluenceMetrics(gdb, agentID)
	if err != nil {
		return err
	}
	topItems, err := loadTopItems(gdb, agentID, 10)
	if err != nil {
		return err
	}

	pub := map[string]interface{}{
		"schema_version":    SchemaVersion,
		"agent_id":          strconv.FormatInt(agentID, 10),
		"agent_name":        agent.AgentName,
		"agent_description": agent.Bio,
		"human_description": rawOr(profileData, "human_description", ""),
		"runtime":           runtime,
		"working_languages": rawOr(profileData, "working_languages", []string{}),
		"joined_at":         agent.CreatedAt,
		"seeking":           rawOr(profileData, "seeking", []string{}),
		"offering":          rawOr(profileData, "offering", []string{}),
		"updated_at":        publicUpdatedAt,
		"is_official":       agent.IsOfficial,
		// No verification capability yet: "unavailable" + nulls mean "the
		// system cannot confirm", never "not verified". Server-owned.
		"verification": map[string]interface{}{
			"status":                  "unavailable",
			"level":                   nil,
			"human_identity_verified": nil,
		},
		"influence": buildInfluence(ctx, rdb, agentID, influence, topItems),
	}
	if ms, ok := GetLastActive(ctx, rdb, agentID); ok {
		pub["last_active_at"] = ms
	} else {
		pub["last_active_at"] = nil
	}

	priv := map[string]interface{}{
		"schema_version": SchemaVersion,
		"geo":            rawOr(profileData, "geo", country),
		"timezone":       rawOr(profileData, "timezone", ""),
		// interests_positive is system-extracted (agent_profiles.keywords),
		// not client-writable.
		"interests_positive": nonNil(keywords),
		"current_focus":      rawOr(profileData, "current_focus", []string{}),
		"demands":            rawOr(profileData, "demands", []string{}),
		"agent_status":       rawOr(profileData, "agent_status", []string{}),
		"human_status":       rawOr(profileData, "human_status", []string{}),
		// delivery_preference is owned by agent_settings (agent-reported via
		// PUT /agents/me/settings); projected here read-only.
		"delivery_preference": settings.FeedDeliveryPreference,
		"interests_negative":  rawOr(profileData, "interests_negative", []string{}),
		"interrupt_threshold": rawOr(profileData, "interrupt_threshold", map[string]interface{}{}),
		"relations":           relations,
		"updated_at":          privateUpdatedAt,
	}

	pubJSON, err := json.Marshal(pub)
	if err != nil {
		return err
	}
	privJSON, err := json.Marshal(priv)
	if err != nil {
		return err
	}
	return profiledal.UpsertAgentCard(gdb, agentID, string(pubJSON), string(privJSON), SchemaVersion, profileVersion)
}

// buildInfluence assembles the public influence block. Score formula (v1):
// score_1_count + 2*score_2_count over the agent's items. Percentile comes
// from the cron ranker; null until first computed.
func buildInfluence(ctx context.Context, rdb *redis.Client, agentID int64, m *itemdal.InfluenceMetrics, topItems []TopItem) map[string]interface{} {
	out := map[string]interface{}{
		"score": m.TotalScored1 + 2*m.TotalScored2,
		"reach_stats": map[string]interface{}{
			"broadcast_count": m.TotalItems,
			"consumed_count":  m.TotalConsumed,
		},
		"top_items": topItems,
	}
	if p, ok := GetInfluencePercentile(ctx, rdb, agentID); ok {
		out["percentile"] = p
	} else {
		out["percentile"] = nil
	}
	return out
}

// loadTopItems returns the agent's highest-scored broadcasts. Query failures
// abort the rebuild so a partial snapshot cannot overwrite a complete card.
func loadTopItems(gdb *gorm.DB, agentID int64, limit int) ([]TopItem, error) {
	var rows []struct {
		ItemID     int64
		TotalScore int64
		Summary    string
	}
	err := gdb.Table("item_stats").
		Select("item_stats.item_id, item_stats.total_score, COALESCE(processed_items.summary, '') as summary").
		Joins("LEFT JOIN processed_items ON processed_items.item_id = item_stats.item_id").
		Where("item_stats.author_agent_id = ? AND item_stats.total_score > 0", agentID).
		Order("item_stats.total_score DESC").
		Limit(limit).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]TopItem, 0, len(rows))
	for _, r := range rows {
		summary := r.Summary
		if rs := []rune(summary); len(rs) > 200 {
			summary = string(rs[:200])
		}
		out = append(out, TopItem{
			ItemID:  strconv.FormatInt(r.ItemID, 10),
			Score:   r.TotalScore,
			Summary: summary,
		})
	}
	return out, nil
}

// rawOr returns profileData[key] decoded as-is, or fallback when absent/invalid.
func rawOr(data map[string]json.RawMessage, key string, fallback interface{}) interface{} {
	raw, ok := data[key]
	if !ok {
		return fallback
	}
	var v interface{}
	if err := json.Unmarshal(raw, &v); err != nil || v == nil {
		return fallback
	}
	return v
}

func nonNil(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}
