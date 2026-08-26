package agentcard

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"eigenflux_server/pkg/agentidentity"
	"eigenflux_server/pkg/feedpoll"
	"eigenflux_server/pkg/runtimeidentity"
	itemdal "eigenflux_server/rpc/item/dal"
	pmdal "eigenflux_server/rpc/pm/dal"
	profiledal "eigenflux_server/rpc/profile/dal"
)

// ErrAgentNotFound is returned by Rebuild for unknown agent ids.
var ErrAgentNotFound = errors.New("agentcard: agent not found")

const rebuildLockTTL = 2 * time.Minute

// TopItem is one entry of influence.top_items.
type TopItem struct {
	ItemID  string `json:"item_id"`
	Score   int64  `json:"score"`
	Summary string `json:"summary,omitempty"`
}

type agentInfluenceFacts struct {
	Score          int64
	BroadcastCount int64
	ConsumedCount  int64
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
// (relations, keywords, influence), the per-agent Redis lock serializes all
// rebuild entry points so an older equal-version projection cannot finish last.
func Rebuild(ctx context.Context, gdb *gorm.DB, rdb *redis.Client, agentID int64) error {
	return rebuildAgentCard(ctx, gdb, rdb, agentID, 0, false)
}

// RebuildWithFence uses a fence allocated before a larger reconciliation run
// read its shared inputs. A cron lease holder that resumes after expiry cannot
// obtain a newer per-agent fence and publish an older ranking.
func RebuildWithFence(ctx context.Context, gdb *gorm.DB, rdb *redis.Client, agentID, rebuildFence int64) error {
	if rebuildFence <= 0 {
		return fmt.Errorf("agentcard: invalid rebuild fence %d", rebuildFence)
	}
	return rebuildAgentCard(ctx, gdb, rdb, agentID, rebuildFence, false)
}

// RebuildOnMiss serializes cache-miss repair and rechecks the projection after
// acquiring the lock. Concurrent readers therefore cause one rebuild, not a
// queue of identical rebuilds.
func RebuildOnMiss(ctx context.Context, gdb *gorm.DB, rdb *redis.Client, agentID int64) error {
	return rebuildAgentCard(ctx, gdb, rdb, agentID, 0, true)
}

func rebuildAgentCard(ctx context.Context, gdb *gorm.DB, rdb *redis.Client, agentID, rebuildFence int64, onlyIfMissing bool) error {
	lockedCtx, release, err := acquireRebuildLock(ctx, rdb, agentID)
	if err != nil {
		return err
	}
	defer release()
	gdb = gdb.WithContext(lockedCtx)
	if onlyIfMissing {
		card, cardErr := profiledal.GetAgentCard(gdb, agentID)
		if cardErr == nil && card.SchemaVersion == SchemaVersion {
			return nil
		}
		if cardErr != nil && !errors.Is(cardErr, gorm.ErrRecordNotFound) {
			return cardErr
		}
	}
	// Redis serializes the normal path, but it is only a lease: a paused
	// holder can resume after its lease expires. Take a database-monotonic
	// fence before reading facts so a later lock holder always wins the final
	// write even if the old holder reaches the upsert afterwards.
	if rebuildFence == 0 {
		rebuildFence, err = profiledal.NextAgentCardRebuildFence(gdb)
		if err != nil {
			return err
		}
	}
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
		ClientHost              string
		Mode                    string
		RuntimeName             string
		RuntimeVersion          string
		CLIVersion              string
		FeedDeliveryPreference  string
		FeedPollInterval        int32
		FeedPollIntervalUserSet bool
		AgentCreatedAtMs        int64
		UpdatedAt               int64
	}
	if err := gdb.Table("agent_settings").
		Select("client_host, mode, runtime_name, runtime_version, cli_version, feed_delivery_preference, feed_poll_interval, feed_poll_interval_user_set, agent_created_at_ms, updated_at").
		Where("agent_id = ?", agentID).
		Scan(&settings).Error; err != nil {
		return err
	}
	runtime, runtimeMode, runtimeName, runtimeVersion := cardRuntimeFields(
		settings.Mode,
		settings.ClientHost,
		settings.RuntimeName,
		settings.RuntimeVersion,
		settings.CLIVersion,
	)
	if runtime != "" && settings.UpdatedAt > publicUpdatedAt {
		publicUpdatedAt = settings.UpdatedAt
	}
	if (runtimeMode != "" || runtimeName != "" || runtimeVersion != "") && settings.UpdatedAt > publicUpdatedAt {
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

	influence, err := loadAgentInfluence(gdb, agentID)
	if err != nil {
		return err
	}
	topItems, err := loadTopItems(gdb, agentID, 10)
	if err != nil {
		return err
	}

	percentile, percentileOK, err := GetInfluencePercentileStrict(lockedCtx, rdb, agentID)
	if err != nil {
		return fmt.Errorf("read influence percentile: %w", err)
	}
	lastActive, lastActiveOK, err := GetLastActiveStrict(lockedCtx, rdb, agentID)
	if err != nil {
		return fmt.Errorf("read last active: %w", err)
	}

	pub := map[string]interface{}{
		"schema_version":    SchemaVersion,
		"agent_id":          strconv.FormatInt(agentID, 10),
		"short_id":          agent.ShortID,
		"display_name":      agentidentity.DisplayName(agent.AgentName, agent.ShortID),
		"agent_name":        agent.AgentName,
		"agent_description": agent.Bio,
		"human_description": rawOr(profileData, "human_description", ""),
		"runtime":           runtime,
		"runtime_mode":      runtimeMode,
		"runtime_name":      runtimeName,
		"runtime_version":   runtimeVersion,
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
		"influence": buildInfluence(influence, topItems, percentile, percentileOK),
	}
	if lastActiveOK {
		pub["last_active_at"] = lastActive
	} else {
		pub["last_active_at"] = nil
	}

	pollCreatedAtMs := settings.AgentCreatedAtMs
	if pollCreatedAtMs <= 0 {
		pollCreatedAtMs = agent.CreatedAt
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
		// interrupt_threshold is the effective feed-pull cadence in seconds.
		// agent_settings is the fact source; clients cannot patch this projection.
		"interrupt_threshold": feedpoll.EffectiveInterval(settings.FeedPollInterval, settings.FeedPollIntervalUserSet, pollCreatedAtMs, time.Now().UnixMilli()),
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
	return profiledal.UpsertAgentCardWithFence(gdb, agentID, string(pubJSON), string(privJSON), SchemaVersion, profileVersion, rebuildFence)
}

func cardRuntimeFields(mode, clientHost, runtimeName, runtimeVersion, cliVersion string) (legacy, runtimeMode, name, version string) {
	legacy = mode
	if mode == "plugin" && clientHost != "" {
		legacy = clientHost
	} else if legacy == "" {
		legacy = clientHost
	}
	runtimeMode = mode
	if runtimeMode == "" && cliVersion != "" {
		runtimeMode = "cli-direct"
	}
	name, version = runtimeName, runtimeVersion
	if name == "" {
		if identity, ok := runtimeidentity.Parse(clientHost); ok {
			name, version = identity.Name, identity.Version
		}
	}
	return legacy, runtimeMode, name, version
}

func acquireRebuildLock(ctx context.Context, rdb *redis.Client, agentID int64) (context.Context, func(), error) {
	if rdb == nil {
		return ctx, func() {}, nil
	}
	var tokenBytes [16]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		return nil, nil, err
	}
	token := hex.EncodeToString(tokenBytes[:])
	key := "lock:agentcard:rebuild:" + strconv.FormatInt(agentID, 10)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(10 * time.Second)
	defer timeout.Stop()
	for {
		ok, err := rdb.SetNX(ctx, key, token, rebuildLockTTL).Result()
		if err != nil {
			return nil, nil, fmt.Errorf("acquire rebuild lock: %w", err)
		}
		if ok {
			lockedCtx, cancelLocked := context.WithCancel(ctx)
			renewCtx, stopRenewal := context.WithCancel(lockedCtx)
			go func() {
				ticker := time.NewTicker(rebuildLockTTL / 3)
				defer ticker.Stop()
				const renewScript = `if redis.call("GET", KEYS[1]) == ARGV[1] then return redis.call("PEXPIRE", KEYS[1], ARGV[2]) else return 0 end`
				for {
					select {
					case <-renewCtx.Done():
						return
					case <-ticker.C:
						renewed, err := rdb.Eval(renewCtx, renewScript, []string{key}, token, rebuildLockTTL.Milliseconds()).Int64()
						if err != nil || renewed == 0 {
							cancelLocked()
							return
						}
					}
				}
			}()
			return lockedCtx, func() {
				stopRenewal()
				cancelLocked()
				releaseCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				const script = `if redis.call("GET", KEYS[1]) == ARGV[1] then return redis.call("DEL", KEYS[1]) else return 0 end`
				_ = rdb.Eval(releaseCtx, script, []string{key}, token).Err()
			}, nil
		}
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-timeout.C:
			return nil, nil, fmt.Errorf("agentcard: rebuild lock timeout for agent %d", agentID)
		case <-ticker.C:
		}
	}
}

// buildInfluence assembles the public influence block. Score formula (v1):
// score_1_count + 2*score_2_count over the agent's items. Percentile comes
// from the cron ranker; null until first computed.
func buildInfluence(m agentInfluenceFacts, topItems []TopItem, percentile int, percentileOK bool) map[string]interface{} {
	out := map[string]interface{}{
		"score": m.Score,
		"reach_stats": map[string]interface{}{
			"broadcast_count": m.BroadcastCount,
			"consumed_count":  m.ConsumedCount,
		},
		"top_items": topItems,
	}
	if percentileOK {
		out["percentile"] = percentile
	} else {
		out["percentile"] = nil
	}
	return out
}

func loadAgentInfluence(gdb *gorm.DB, agentID int64) (agentInfluenceFacts, error) {
	var row struct {
		Score          int64
		BroadcastCount int64
		ConsumedCount  int64
		Ready          bool
	}
	err := gdb.Raw(`SELECT meta.backfill_complete AS ready,
		COALESCE(SUM(score_1_count) + 2 * SUM(score_2_count), 0)::BIGINT AS score,
		COALESCE(SUM(broadcast_count), 0)::BIGINT AS broadcast_count,
		COALESCE(SUM(consumed_count), 0)::BIGINT AS consumed_count
	FROM agent_influence_rollup_meta AS meta
	LEFT JOIN agent_influence_rollups AS rollup ON rollup.agent_id = ?
	WHERE meta.singleton = TRUE GROUP BY meta.backfill_complete`, agentID).Scan(&row).Error
	if err != nil {
		return agentInfluenceFacts{}, err
	}
	if !row.Ready {
		return agentInfluenceFacts{}, fmt.Errorf("agentcard: influence rollup backfill is incomplete")
	}
	return agentInfluenceFacts{
		Score:          row.Score,
		BroadcastCount: row.BroadcastCount,
		ConsumedCount:  row.ConsumedCount,
	}, nil
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
		Where("item_stats.author_agent_id = ? AND item_stats.total_score > 0 AND processed_items.status = ?", agentID, itemdal.StatusCompleted).
		Order("item_stats.total_score DESC, item_stats.item_id ASC").
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
	spec, ok := LookupField(key)
	if !ok {
		return fallback
	}
	v, err := ValidateValue(spec, raw)
	if err != nil || v == nil {
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
