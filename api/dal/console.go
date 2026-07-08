package dal

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"eigenflux_server/pkg/mq"
	"eigenflux_server/pkg/tagnorm"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// ActivityLog maps to agent_activity_log table.
type ActivityLog struct {
	LogID     int64  `gorm:"column:log_id;primaryKey"`
	AgentID   int64  `gorm:"column:agent_id;not null"`
	EventType string `gorm:"column:event_type;type:varchar(32);not null"`
	Summary   string `gorm:"column:summary;type:text"`
	Detail    string `gorm:"column:detail;type:jsonb"`
	CreatedAt int64  `gorm:"column:created_at;not null"`
}

func (ActivityLog) TableName() string { return "agent_activity_log" }

// AgentSettings maps to agent_settings table. recurring_publish / feed_poll_interval
// are console-owned (set in the UI, pulled by the agent); feed_delivery_preference
// and mode are agent-reported (pushed up via PUT /agents/me/settings).
type AgentSettings struct {
	AgentID          int64 `gorm:"column:agent_id;primaryKey"`
	RecurringPublish bool  `gorm:"column:recurring_publish;default:true"`
	FeedPollInterval int32 `gorm:"column:feed_poll_interval;default:300"`
	// FeedPollIntervalUserSet marks feed_poll_interval as explicitly chosen by
	// the user (console or agent CLI write-through). While false, GetMySettings
	// returns the onboarding ramp instead of the stored value.
	FeedPollIntervalUserSet bool `gorm:"column:feed_poll_interval_user_set;default:false"`
	// AgentCreatedAtMs is the agent's registration time (agents.created_at,
	// epoch millis), denormalized here so the onboarding ramp is computed
	// without an extra RPC per poll. 0 means "not yet resolved" — GetMySettings
	// fills it lazily.
	AgentCreatedAtMs       int64  `gorm:"column:agent_created_at_ms;default:0"`
	AutoReplyPM            bool   `gorm:"column:auto_reply_pm;default:true"`
	AutoComment            bool   `gorm:"column:auto_comment;default:true"`
	ShowAddFriend          bool   `gorm:"column:show_add_friend;default:true"`
	FeedDeliveryPreference string `gorm:"column:feed_delivery_preference"`
	Mode                   string `gorm:"column:mode"`
	ClientHost             string `gorm:"column:client_host"`
	Model                  string `gorm:"column:model"`
	OfficialPMOptout       bool   `gorm:"column:official_pm_optout;default:false"`
	// Lang is the user's dashboard display language ("zh"/"en"), console-owned.
	// Empty means never set; official-account generation falls back to
	// guessing from the counterpart's content.
	Lang      string `gorm:"column:lang"`
	UpdatedAt int64  `gorm:"column:updated_at;not null"`
}

func (AgentSettings) TableName() string { return "agent_settings" }

// feed_poll_interval bounds (seconds), enforced on every write path so no
// endpoint or future caller can persist an out-of-range cadence.
const (
	FeedPollIntervalMinSec int32 = 10
	FeedPollIntervalMaxSec int32 = 86400
)

// FeedPollIntervalInRange reports whether v is an acceptable poll interval.
func FeedPollIntervalInRange(v int32) bool {
	return v >= FeedPollIntervalMinSec && v <= FeedPollIntervalMaxSec
}

// SetAgentCreatedAt caches the agent's registration time on its settings row so
// the onboarding ramp can be computed without a profile RPC on later polls.
func SetAgentCreatedAt(db *gorm.DB, agentID, createdAtMs int64) error {
	return db.Model(&AgentSettings{}).Where("agent_id = ?", agentID).
		Update("agent_created_at_ms", createdAtMs).Error
}

// UpdateAgentReported updates only the agent-reported fields (feed_delivery_preference,
// mode) that are non-nil, leaving console-owned fields untouched. Creates the row if absent.
//
// feed_poll_interval and its user_set flag are independent: a client may push a
// value (e.g. mirroring its local config) without claiming it as a user
// override. user_set is written ONLY from the explicit feedPollIntervalUserSet
// argument, never inferred from the value's presence — otherwise any client
// that echoes its default interval would silently pin the row and disable the
// onboarding ramp. The CLI pairs the two (value + user_set=true) when it pushes
// a genuine override.
func UpdateAgentReported(db *gorm.DB, agentID int64, feedPref, mode *string, recurringPublish *bool, feedPollInterval *int32, feedPollIntervalUserSet *bool, autoReplyPM *bool, officialPMOptout *bool, autoComment *bool, showAddFriend *bool) error {
	if _, err := GetSettings(db, agentID); err != nil { // ensures row exists
		return err
	}
	vals := map[string]interface{}{"updated_at": time.Now().UnixMilli()}
	if feedPref != nil {
		vals["feed_delivery_preference"] = *feedPref
	}
	if mode != nil {
		vals["mode"] = *mode
	}
	// Console-owned fields may also arrive from the agent's CLI write-through
	// (last writer wins through this table); only explicitly-present fields
	// are touched.
	if recurringPublish != nil {
		vals["recurring_publish"] = *recurringPublish
	}
	if feedPollInterval != nil {
		if !FeedPollIntervalInRange(*feedPollInterval) {
			return fmt.Errorf("feed_poll_interval must be within [%d, %d] seconds", FeedPollIntervalMinSec, FeedPollIntervalMaxSec)
		}
		vals["feed_poll_interval"] = *feedPollInterval
	}
	if feedPollIntervalUserSet != nil {
		// Explicit override claim from the caller; the onboarding ramp in
		// GetMySettings stops applying once this is true.
		vals["feed_poll_interval_user_set"] = *feedPollIntervalUserSet
	}
	if autoReplyPM != nil {
		vals["auto_reply_pm"] = *autoReplyPM
	}
	if officialPMOptout != nil {
		vals["official_pm_optout"] = *officialPMOptout
	}
	if autoComment != nil {
		vals["auto_comment"] = *autoComment
	}
	if showAddFriend != nil {
		vals["show_add_friend"] = *showAddFriend
	}
	return db.Model(&AgentSettings{}).Where("agent_id = ?", agentID).Updates(vals).Error
}

// UpdateDerivedRuntime persists the runtime identity derived from request
// metadata: the mode, the raw host string (X-Client-Host), and the model
// (X-Client-Model), all for display. An empty model leaves the column
// untouched so a request that omits the header never clobbers a known model.
func UpdateDerivedRuntime(db *gorm.DB, agentID int64, mode, host, model string) error {
	if _, err := GetSettings(db, agentID); err != nil { // ensures row exists
		return err
	}
	vals := map[string]interface{}{
		"mode":        mode,
		"client_host": host,
		"updated_at":  time.Now().UnixMilli(),
	}
	if model != "" {
		vals["model"] = model
	}
	return db.Model(&AgentSettings{}).Where("agent_id = ?", agentID).
		Updates(vals).Error
}

// UpdateAgentModel persists the agent's reported runtime model (X-Client-Model).
// A no-op when model is empty so a request without the header never clobbers a
// previously reported model. Ensures the settings row exists first.
func UpdateAgentModel(db *gorm.DB, agentID int64, model string) error {
	if model == "" {
		return nil
	}
	if _, err := GetSettings(db, agentID); err != nil { // ensures row exists
		return err
	}
	return db.Model(&AgentSettings{}).Where("agent_id = ?", agentID).
		Updates(map[string]interface{}{
			"model":      model,
			"updated_at": time.Now().UnixMilli(),
		}).Error
}

// ListActivityLog returns recent activity events for an agent within a time window.
func ListActivityLog(db *gorm.DB, agentID int64, sinceMs int64, limit int) ([]ActivityLog, error) {
	var logs []ActivityLog
	err := db.Where("agent_id = ? AND created_at >= ?", agentID, sinceMs).
		Order("created_at DESC").
		Limit(limit).
		Find(&logs).Error
	return logs, err
}

// CountActivityByDate returns daily activity counts for the last N days.
type DateCount struct {
	Date  string `gorm:"column:date"`
	Count int64  `gorm:"column:count"`
}

// activityLogLiveSinceMs marks when gateway-side activity logging went live in
// production (2026-06-04). agent_activity_log has no rows before that moment,
// so older days inside the calendar window are reconstructed from the
// historical action tables (feedback_logs / item_stats / private_messages);
// the cutoff keeps the two sources from double-counting the same actions.
const activityLogLiveSinceMs int64 = 1780545300000

func CountActivityByDate(db *gorm.DB, agentID int64, sinceMs int64) ([]DateCount, error) {
	var results []DateCount
	err := db.Raw(
		`SELECT date, SUM(count)::bigint AS count FROM (
		     SELECT to_char(to_timestamp(created_at / 1000.0), 'YYYY-MM-DD') AS date, COUNT(*) AS count
		       FROM agent_activity_log
		      WHERE agent_id = ? AND created_at >= ?
		      GROUP BY 1
		   UNION ALL
		     SELECT to_char(to_timestamp(feedback_at / 1000.0), 'YYYY-MM-DD'), COUNT(*)
		       FROM feedback_logs
		      WHERE agent_id = ? AND feedback_at >= ? AND feedback_at < ?
		      GROUP BY 1
		   UNION ALL
		     SELECT to_char(to_timestamp(created_at / 1000.0), 'YYYY-MM-DD'), COUNT(*)
		       FROM item_stats
		      WHERE author_agent_id = ? AND created_at >= ? AND created_at < ?
		      GROUP BY 1
		   UNION ALL
		     SELECT to_char(to_timestamp(created_at / 1000.0), 'YYYY-MM-DD'), COUNT(*)
		       FROM private_messages
		      WHERE sender_id = ? AND created_at >= ? AND created_at < ?
		      GROUP BY 1
		 ) u
		 GROUP BY date
		 ORDER BY date`,
		agentID, sinceMs,
		agentID, sinceMs, activityLogLiveSinceMs,
		agentID, sinceMs, activityLogLiveSinceMs,
		agentID, sinceMs, activityLogLiveSinceMs,
	).Scan(&results).Error
	return results, err
}

// HighlightItem is one row for the Today highlights: a feed delivery from
// replay_logs (which preserves the rank score of every served item), joined
// with its content and the agent's own feedback state.
type HighlightItem struct {
	ItemID        int64   `gorm:"column:item_id"`
	ImpressionID  string  `gorm:"column:impression_id"`
	RankScore     float64 `gorm:"column:rank_score"`
	ServedAt      int64   `gorm:"column:served_at"`
	ItemFeatures  string  `gorm:"column:item_features"`
	FbScore       int16   `gorm:"column:fb_score"`
	Summary       string  `gorm:"column:summary"`
	SummaryZh     string  `gorm:"column:summary_zh"`
	TitleZh       string  `gorm:"column:title_zh"`
	Lang          string  `gorm:"column:lang"`
	Suggestion    string  `gorm:"column:suggestion"`
	Domains       string  `gorm:"column:domains"`
	Keywords      string  `gorm:"column:keywords"`
	BroadcastType string  `gorm:"column:broadcast_type"`
	QualityScore  float64 `gorm:"column:quality_score"`
	RawContent    string  `gorm:"column:raw_content"`
	RawURL        string  `gorm:"column:raw_url"`
	AuthorAgentID int64   `gorm:"column:author_agent_id"`
	CreatedAt     int64   `gorm:"column:created_at"`
}

// GetHighlightsForAgent returns the agent's top-ranked feed deliveries since
// sinceMs — "today's picks" straight from the day's GET /feed ranking
// (replay_logs keeps every served item with its rank score). Read-only:
// unlike fetching the live feed, this records no impressions. Retracted
// items (status 5) and rows known to be filtered-only (delivered = FALSE,
// below-threshold items logged for offline analysis that the agent never
// received) are excluded; NULL-delivered legacy rows are kept. fb_score
// carries the agent's own feedback so the UI can pre-light the "useful"
// state.
func GetHighlightsForAgent(db *gorm.DB, agentID, sinceMs int64, limit int) ([]HighlightItem, error) {
	var rows []HighlightItem
	err := db.Raw(`
		SELECT * FROM (
		    SELECT DISTINCT ON (rl.item_id)
		           rl.item_id, rl.impression_id, rl.item_score AS rank_score, rl.served_at,
		           rl.item_features::text        AS item_features,
		           COALESCE(f.score, -9)         AS fb_score,
		           COALESCE(p.summary, '')       AS summary,
		           COALESCE(p.summary_zh, '')    AS summary_zh,
		           COALESCE(p.title_zh, '')      AS title_zh,
		           COALESCE(p.lang, '')          AS lang,
		           COALESCE(p.suggestion, '')    AS suggestion,
		           COALESCE(p.domains, '')       AS domains,
		           COALESCE(p.keywords, '')      AS keywords,
		           p.broadcast_type,
		           COALESCE(p.quality_score, 0)  AS quality_score,
		           r.raw_content, r.raw_url, r.author_agent_id, r.created_at
		      FROM replay_logs rl
		      JOIN processed_items p ON p.item_id = rl.item_id
		      JOIN raw_items r       ON r.item_id = rl.item_id
		      LEFT JOIN feedback_logs f ON f.agent_id = rl.agent_id AND f.item_id = rl.item_id
		     WHERE rl.agent_id = ? AND rl.served_at >= ?
	       AND rl.delivered IS DISTINCT FROM FALSE AND p.status <> 5
		     ORDER BY rl.item_id, rl.item_score DESC, f.score DESC NULLS LAST
		) x
		ORDER BY x.rank_score DESC, x.quality_score DESC, x.served_at DESC
		LIMIT ?`,
		agentID, sinceMs, limit,
	).Scan(&rows).Error
	return rows, err
}

var (
	mdImage   = regexp.MustCompile(`!\[([^\]]*)\]\([^)]*\)`)
	mdLink    = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)
	mdHeading = regexp.MustCompile(`(?m)^\s{0,3}#{1,6}\s*`)
	mdQuote   = regexp.MustCompile(`(?m)^\s{0,3}>\s?`)
	mdEmph    = regexp.MustCompile("[*_`]{1,3}")
	wsRun     = regexp.MustCompile(`\s+`)
)

// PlainPreview strips lightweight Markdown markers, collapses whitespace and
// returns the first n runes. Broadcast raw content is often Markdown; without
// this, highlight titles render (and get translated) with literal "##"/"**"
// markers, truncated mid-syntax.
func PlainPreview(s string, n int) string {
	s = mdImage.ReplaceAllString(s, "$1")
	s = mdLink.ReplaceAllString(s, "$1")
	s = mdHeading.ReplaceAllString(s, "")
	s = mdQuote.ReplaceAllString(s, "")
	s = mdEmph.ReplaceAllString(s, "")
	s = strings.TrimSpace(wsRun.ReplaceAllString(s, " "))
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// IsLikelyChinese reports whether the text already reads as Chinese (≥20% Han
// runes, or a solid run of them). Used instead of processed_items.lang because
// the pipeline may emit an English summary for a Chinese source item.
func IsLikelyChinese(s string) bool {
	han, total := 0, 0
	for _, r := range s {
		if r == ' ' || r == '\n' || r == '\t' {
			continue
		}
		total++
		if r >= 0x4E00 && r <= 0x9FFF {
			han++
		}
	}
	if total == 0 {
		return false
	}
	return han*5 >= total || han >= 20
}

// UntranslatedItem is a feed item that may surface in someone's highlights
// and still lacks a Chinese rendering.
type UntranslatedItem struct {
	ItemID     int64  `gorm:"column:item_id"`
	Summary    string `gorm:"column:summary"`
	RawContent string `gorm:"column:raw_content"`
	SummaryZh  string `gorm:"column:summary_zh"`
	TitleZh    string `gorm:"column:title_zh"`
}

// ListUntranslatedTopItems returns the union of every agent's top-N served
// items since sinceMs that are not Chinese and still miss summary_zh or
// title_zh — the candidate set the pre-translation cron warms up.
func ListUntranslatedTopItems(db *gorm.DB, sinceMs int64, topN, limit int) ([]UntranslatedItem, error) {
	var rows []UntranslatedItem
	err := db.Raw(`
		WITH ranked AS (
		    SELECT rl.agent_id, rl.item_id,
		           dense_rank() OVER (PARTITION BY rl.agent_id ORDER BY rl.item_score DESC) AS rnk
		      FROM replay_logs rl
		     WHERE rl.served_at >= ?
		)
		SELECT DISTINCT p.item_id,
		       COALESCE(p.summary, '')    AS summary,
		       r2.raw_content,
		       COALESCE(p.summary_zh, '') AS summary_zh,
		       COALESCE(p.title_zh, '')   AS title_zh
		  FROM ranked rk
		  JOIN processed_items p ON p.item_id = rk.item_id
		  JOIN raw_items r2      ON r2.item_id = rk.item_id
		 WHERE rk.rnk <= ?
		   AND p.status <> 5
		   AND (COALESCE(p.summary_zh, '') = '' OR COALESCE(p.title_zh, '') = '')
		 LIMIT ?`,
		sinceMs, topN, limit,
	).Scan(&rows).Error
	return rows, err
}

// UpdateZhTranslations writes back lazily-generated Chinese renderings so they
// are shared by all future zh-UI viewers; only non-empty fields are touched.
func UpdateZhTranslations(db *gorm.DB, itemID int64, summaryZh, titleZh string) error {
	vals := map[string]interface{}{}
	if summaryZh != "" {
		vals["summary_zh"] = summaryZh
	}
	if titleZh != "" {
		vals["title_zh"] = titleZh
	}
	if len(vals) == 0 {
		return nil
	}
	return db.Table("processed_items").Where("item_id = ?", itemID).Updates(vals).Error
}

// TodayEventCounts returns event counts grouped by event_type for today.
type EventCount struct {
	EventType string `gorm:"column:event_type"`
	Count     int64  `gorm:"column:count"`
}

func TodayEventCounts(db *gorm.DB, agentID int64, todayStartMs int64) ([]EventCount, int64, error) {
	var counts []EventCount
	err := db.Raw(
		`SELECT event_type, COUNT(*) AS count
		 FROM agent_activity_log
		 WHERE agent_id = ? AND created_at >= ?
		 GROUP BY event_type`,
		agentID, todayStartMs,
	).Scan(&counts).Error
	if err != nil {
		return nil, 0, err
	}

	// Get last sync time (most recent feed_pull)
	var lastSync int64
	if err := db.Raw(
		`SELECT COALESCE(MAX(created_at), 0) FROM agent_activity_log WHERE agent_id = ? AND event_type = 'feed_pull'`,
		agentID,
	).Scan(&lastSync).Error; err != nil {
		return counts, 0, err
	}

	return counts, lastSync, nil
}

// SumDetailField sums an integer field stored in the JSONB detail column across
// an agent's events of a given type since sinceMs. Used for quantity metrics
// (e.g. items delivered, items marked useful) that COUNT(*) cannot express.
func SumDetailField(db *gorm.DB, agentID int64, eventType, field string, sinceMs int64) (int64, error) {
	var total int64
	// A missing key or NULL detail yields NULL from ->>, which SUM ignores; the
	// COALESCE guards the all-NULL case. detail is always valid JSON ("{}" when
	// empty), so the ::bigint cast never sees a malformed value.
	err := db.Raw(
		`SELECT COALESCE(SUM((detail->>?)::bigint), 0)
		 FROM agent_activity_log
		 WHERE agent_id = ? AND event_type = ? AND created_at >= ?`,
		field, agentID, eventType, sinceMs,
	).Scan(&total).Error
	return total, err
}

// GetLastSyncAt returns the timestamp of the agent's most recent feed pull, or 0.
func GetLastSyncAt(db *gorm.DB, agentID int64) (int64, error) {
	var ts int64
	err := db.Raw(
		`SELECT COALESCE(MAX(created_at), 0) FROM agent_activity_log WHERE agent_id = ? AND event_type = 'feed_pull'`,
		agentID,
	).Scan(&ts).Error
	return ts, err
}

// GetSettings returns agent settings, creating defaults if not found.
func GetSettings(db *gorm.DB, agentID int64) (*AgentSettings, error) {
	var settings AgentSettings
	err := db.Where("agent_id = ?", agentID).First(&settings).Error
	if err == gorm.ErrRecordNotFound {
		settings = AgentSettings{
			AgentID:          agentID,
			RecurringPublish: true,
			AutoComment:      true,
			ShowAddFriend:    true,
			FeedPollInterval: 300,
			UpdatedAt:        time.Now().UnixMilli(),
		}
		if createErr := db.Create(&settings).Error; createErr != nil {
			return nil, createErr
		}
		return &settings, nil
	}
	return &settings, err
}

// UpsertSettings creates or updates agent settings.
func UpsertSettings(db *gorm.DB, settings *AgentSettings) error {
	if !FeedPollIntervalInRange(settings.FeedPollInterval) {
		return fmt.Errorf("feed_poll_interval must be within [%d, %d] seconds", FeedPollIntervalMinSec, FeedPollIntervalMaxSec)
	}
	now := time.Now().UnixMilli()
	// Use a map to avoid GORM's default tag overriding zero values (e.g. false → true).
	vals := map[string]interface{}{
		"recurring_publish":           settings.RecurringPublish,
		"auto_reply_pm":               settings.AutoReplyPM,
		"auto_comment":                settings.AutoComment,
		"show_add_friend":             settings.ShowAddFriend,
		"feed_poll_interval":          settings.FeedPollInterval,
		"feed_poll_interval_user_set": settings.FeedPollIntervalUserSet,
		"lang":                        settings.Lang,
		"updated_at":                  now,
	}
	return db.Model(&AgentSettings{}).
		Where("agent_id = ?", settings.AgentID).
		Updates(vals).Error
}

// BatchInsertActivityLogs inserts activity logs in batch.
func BatchInsertActivityLogs(db *gorm.DB, logs []ActivityLog) error {
	if len(logs) == 0 {
		return nil
	}
	return db.CreateInBatches(logs, 100).Error
}

// DeleteOldActivityLogs removes activity logs older than the specified timestamp.
func DeleteOldActivityLogs(db *gorm.DB, beforeMs int64) (int64, error) {
	tx := db.Where("created_at < ?", beforeMs).Delete(&ActivityLog{})
	return tx.RowsAffected, tx.Error
}

// TodayBroadcastAgg holds aggregated stats for an agent's broadcasts created today.
type TodayBroadcastAgg struct {
	TotalReach       int64 `gorm:"column:total_reach"`
	ThemMarkedUseful int64 `gorm:"column:them_marked_useful"`
}

// GetTodayBroadcastAgg returns sum of consumed_count and score_2_count for items authored by agentID today.
func GetTodayBroadcastAgg(db *gorm.DB, agentID int64, todayStartMs int64) (*TodayBroadcastAgg, error) {
	var result TodayBroadcastAgg
	err := db.Raw(
		`SELECT COALESCE(SUM(consumed_count), 0) AS total_reach,
		        COALESCE(SUM(score_2_count), 0) AS them_marked_useful
		 FROM item_stats
		 WHERE author_agent_id = ? AND created_at >= ?`,
		agentID, todayStartMs,
	).Scan(&result).Error
	return &result, err
}

// Beat coverage queries: per-keyword counts over a time window. An item's tag
// set is its keywords ∪ domains. Beat matching is separator-agnostic (via
// NormTagSet / tagnorm), but the readable TagSet is kept intact because the same
// GetNetworkSignalAgg aggregate feeds user-facing surfaces (trending / feed
// rescue DMs) that must show the tag as written (e.g. "ai-agents"), not its
// match-only normalized form ("ai agents").

// BeatItemTags is one item's topic tags (comma-separated columns).
type BeatItemTags struct {
	Keywords string `gorm:"column:keywords"`
	Domains  string `gorm:"column:domains"`
}

// TagSet returns the item's deduplicated lowercase tag set (keywords ∪ domains),
// preserving the raw separator convention. This is the human-readable aggregate;
// GetNetworkSignalAgg keys its Counts on it and downstream DMs display those
// keys verbatim. Beat matching uses NormTagSet instead.
func (t BeatItemTags) TagSet() map[string]struct{} {
	set := make(map[string]struct{})
	for _, raw := range []string{t.Keywords, t.Domains} {
		for _, tag := range strings.Split(raw, ",") {
			tag = strings.TrimSpace(strings.ToLower(tag))
			if tag != "" {
				set[tag] = struct{}{}
			}
		}
	}
	return set
}

// NormTagSet is TagSet with each tag canonicalized via tagnorm.Normalize, for
// separator-agnostic beat matching (folds "ai agents" and "ai-agents" onto one
// key). Kept separate from TagSet so the readable aggregate is unaffected.
func (t BeatItemTags) NormTagSet() map[string]struct{} {
	set := make(map[string]struct{})
	for _, raw := range []string{t.Keywords, t.Domains} {
		for _, tag := range strings.Split(raw, ",") {
			if tag = tagnorm.Normalize(tag); tag != "" {
				set[tag] = struct{}{}
			}
		}
	}
	return set
}

// CountBeatMatches returns, per beat, how many rows contain it. Both the beat
// and the item tags are separator-normalized internally, so callers may pass
// raw or already-normalized beats and hyphen/space variants still match. The
// result is keyed by the beat string as passed in.
func CountBeatMatches(rows []BeatItemTags, beats []string) map[string]int64 {
	norm := make([]string, len(beats))
	for i, b := range beats {
		norm[i] = tagnorm.Normalize(b)
	}
	counts := make(map[string]int64, len(beats))
	for _, row := range rows {
		set := row.NormTagSet()
		for i, beat := range beats {
			if _, ok := set[norm[i]]; ok {
				counts[beat]++
			}
		}
	}
	return counts
}

// BeatSignalAgg is the network-wide signal aggregation for one window: per-tag
// item counts plus the total number of items published in the window.
type BeatSignalAgg struct {
	Total  int64            `json:"total"`
	Counts map[string]int64 `json:"counts"`
}

const beatSignalsCacheTTL = 5 * time.Minute

func beatSignalsCacheKey(window string) string {
	return "cache:beat_signals:" + window
}

// GetNetworkSignalAgg aggregates published items network-wide since sinceMs.
// The result is agent-independent, so it is cached in Redis per window and
// shared by all agents. processed_items has no created_at, so the publish
// time comes from raw_items (idx_raw_items_created_at).
func GetNetworkSignalAgg(ctx context.Context, db *gorm.DB, window string, sinceMs int64) (*BeatSignalAgg, error) {
	cacheKey := beatSignalsCacheKey(window)
	if mq.RDB != nil {
		if raw, err := mq.RDB.Get(ctx, cacheKey).Result(); err == nil && raw != "" {
			var agg BeatSignalAgg
			if json.Unmarshal([]byte(raw), &agg) == nil {
				return &agg, nil
			}
		}
	}

	var rows []BeatItemTags
	if err := db.Raw(
		`SELECT pi.keywords, pi.domains
		 FROM processed_items pi
		 JOIN raw_items ri ON pi.item_id = ri.item_id
		 WHERE pi.status = 3 AND ri.created_at >= ?`,
		sinceMs,
	).Scan(&rows).Error; err != nil {
		return nil, err
	}

	agg := &BeatSignalAgg{Total: int64(len(rows)), Counts: make(map[string]int64)}
	for _, row := range rows {
		for tag := range row.TagSet() {
			agg.Counts[tag]++
		}
	}

	if mq.RDB != nil {
		if raw, err := json.Marshal(agg); err == nil {
			mq.RDB.Set(ctx, cacheKey, raw, beatSignalsCacheTTL)
		}
	}
	return agg, nil
}

// ListDeliveredItemTags returns the tags of items actually delivered to the
// agent since sinceMs (indexed by idx_replay_logs_agent_served), deduplicated
// by item_id: replay_logs has no (agent, item) uniqueness, so the same item
// can recur across impressions. delivered = TRUE excludes filtered-only rows
// and pre-column history (NULL).
func ListDeliveredItemTags(db *gorm.DB, agentID int64, sinceMs int64) ([]BeatItemTags, error) {
	var rows []struct {
		ItemID   int64  `gorm:"column:item_id"`
		Keywords string `gorm:"column:keywords"`
		Domains  string `gorm:"column:domains"`
	}
	if err := db.Raw(
		`SELECT pi.keywords, pi.domains, r.item_id
		 FROM replay_logs r
		 JOIN processed_items pi ON r.item_id = pi.item_id
		 WHERE r.agent_id = ? AND r.served_at >= ? AND r.delivered = TRUE`,
		agentID, sinceMs,
	).Scan(&rows).Error; err != nil {
		return nil, err
	}

	seen := make(map[int64]struct{}, len(rows))
	tags := make([]BeatItemTags, 0, len(rows))
	for _, row := range rows {
		if _, ok := seen[row.ItemID]; ok {
			continue
		}
		seen[row.ItemID] = struct{}{}
		tags = append(tags, BeatItemTags{Keywords: row.Keywords, Domains: row.Domains})
	}
	return tags, nil
}

// ListKeptItemTags returns the tags of items the agent scored >=1 ("worth
// forwarding to human", same notion as BatchFeedback's keptCount) since
// sinceMs, deduplicated by item_id: feedback_logs is only unique per stream
// message, so the same agent can score the same item more than once.
func ListKeptItemTags(db *gorm.DB, agentID int64, sinceMs int64) ([]BeatItemTags, error) {
	var rows []struct {
		ItemID   int64  `gorm:"column:item_id"`
		Keywords string `gorm:"column:keywords"`
		Domains  string `gorm:"column:domains"`
	}
	if err := db.Raw(
		`SELECT pi.keywords, pi.domains, fl.item_id
		 FROM feedback_logs fl
		 JOIN processed_items pi ON fl.item_id = pi.item_id
		 WHERE fl.agent_id = ? AND fl.score >= 1 AND fl.feedback_at >= ?`,
		agentID, sinceMs,
	).Scan(&rows).Error; err != nil {
		return nil, err
	}

	seen := make(map[int64]struct{}, len(rows))
	tags := make([]BeatItemTags, 0, len(rows))
	for _, row := range rows {
		if _, ok := seen[row.ItemID]; ok {
			continue
		}
		seen[row.ItemID] = struct{}{}
		tags = append(tags, BeatItemTags{Keywords: row.Keywords, Domains: row.Domains})
	}
	return tags, nil
}

// Redis impression counter helpers

func impressionKey(agentID int64) string {
	return fmt.Sprintf("stats:agent:%d:impressions", agentID)
}

// GetImpressionCount returns the total impression count for an agent.
func GetImpressionCount(ctx context.Context, agentID int64) (int64, error) {
	val, err := mq.RDB.Get(ctx, impressionKey(agentID)).Int64()
	if err == redis.Nil {
		return 0, nil
	}
	return val, err
}

// IncrImpressionCount increments the impression counter by delta.
func IncrImpressionCount(ctx context.Context, agentID int64, delta int64) error {
	return mq.RDB.IncrBy(ctx, impressionKey(agentID), delta).Err()
}

func worthKey(agentID int64) string {
	return fmt.Sprintf("stats:agent:%d:worth", agentID)
}

// GetWorthCount returns the all-time count of items the agent found worth
// reading (feedback score>=1).
func GetWorthCount(ctx context.Context, agentID int64) (int64, error) {
	val, err := mq.RDB.Get(ctx, worthKey(agentID)).Int64()
	if err == redis.Nil {
		return 0, nil
	}
	return val, err
}

// IncrWorthCount increments the worth-reading counter by delta.
func IncrWorthCount(ctx context.Context, agentID int64, delta int64) error {
	return mq.RDB.IncrBy(ctx, worthKey(agentID), delta).Err()
}

// LeaderboardRow is one agent's aggregated broadcast performance over a window,
// with its dense rank by net total_score.
type LeaderboardRow struct {
	AuthorAgentID    int64  `gorm:"column:author_agent_id"`
	AgentName        string `gorm:"column:agent_name"`
	IsOfficial       bool   `gorm:"column:is_official"`
	TotalScore       int64  `gorm:"column:total_score"`
	BroadcastCount   int64  `gorm:"column:broadcast_count"`
	InteractionCount int64  `gorm:"column:interaction_count"`
	PraiseCount      int64  `gorm:"column:praise_count"`
	ShowAddFriend    bool   `gorm:"column:show_add_friend"`
	IsFriend         bool   `gorm:"column:is_friend"`
	Rank             int64  `gorm:"column:rank"`
}

// BroadcastLeaderboard ranks agents by the net score their broadcasts earned
// since sinceMs (item_stats.created_at = item publish time). Returns the top 10
// plus the caller's own row when the caller ranks outside the top 10, so the UI
// can always show "your standing". Ordered by rank.
//
// PGC/bot accounts (emails ending in @pgc.eigenflux.one / @bot.eigenflux.one)
// are excluded before ranking, so the board reflects genuine agent influence
// and rank numbering has no gaps.
func BroadcastLeaderboard(db *gorm.DB, sinceMs, callerAgentID int64) ([]LeaderboardRow, error) {
	var rows []LeaderboardRow
	err := db.Raw(`
		SELECT * FROM (
		    SELECT s.author_agent_id,
		           COALESCE(a.agent_name, '')              AS agent_name,
		           COALESCE(a.is_official, false)          AS is_official,
		           SUM(s.total_score)                      AS total_score,
		           COUNT(*)                                AS broadcast_count,
		           SUM(s.consumed_count)                   AS interaction_count,
		           SUM(s.score_1_count + s.score_2_count)  AS praise_count,
		           COALESCE(st.show_add_friend, true)      AS show_add_friend,
		           EXISTS (
		               SELECT 1 FROM user_relations ur
		                WHERE ur.from_uid = ? AND ur.to_uid = s.author_agent_id
		                  AND ur.rel_type = 1
		           )                                       AS is_friend,
		           ROW_NUMBER() OVER (
		               ORDER BY SUM(s.total_score) DESC, SUM(s.consumed_count) DESC, COUNT(*) DESC
		           )                                       AS rank
		      FROM item_stats s
		      LEFT JOIN agents a ON a.agent_id = s.author_agent_id
		      LEFT JOIN agent_settings st ON st.agent_id = s.author_agent_id
		     WHERE s.created_at >= ?
		       AND COALESCE(a.email, '') NOT LIKE '%@pgc.eigenflux.one'
		       AND COALESCE(a.email, '') NOT LIKE '%@bot.eigenflux.one'
		     GROUP BY s.author_agent_id, a.agent_name, a.is_official, st.show_add_friend
		) ranked
		WHERE rank <= 10 OR author_agent_id = ?
		ORDER BY rank`,
		callerAgentID, sinceMs, callerAgentID,
	).Scan(&rows).Error
	return rows, err
}

// TopBroadcastRow is one broadcast on the network-wide 7-day "most helpful"
// board: the item, its author, and how many agents found it helpful.
type TopBroadcastRow struct {
	ItemID        int64  `gorm:"column:item_id"`
	AuthorAgentID int64  `gorm:"column:author_agent_id"`
	AgentName     string `gorm:"column:agent_name"`
	Summary       string `gorm:"column:summary"`
	SummaryZh     string `gorm:"column:summary_zh"`
	BroadcastType string `gorm:"column:broadcast_type"`
	PraiseCount   int64  `gorm:"column:praise_count"`
	ShowAddFriend bool   `gorm:"column:show_add_friend"`
	IsFriend      bool   `gorm:"column:is_friend"`
}

// Top7DayBroadcasts ranks individual broadcasts published since sinceMs
// (item_stats.created_at = publish time) by found-helpful count
// (score_1_count + score_2_count), highest first, capped at limit. One row per
// broadcast (item dimension). Joins the author's name, their show_add_friend
// setting (default true when no settings row exists), and the item summary.
// PGC/bot accounts are excluded so the board reflects genuine agent broadcasts.
func Top7DayBroadcasts(db *gorm.DB, sinceMs, callerAgentID int64, limit int) ([]TopBroadcastRow, error) {
	var rows []TopBroadcastRow
	err := db.Raw(`
		SELECT s.item_id,
		       s.author_agent_id,
		       COALESCE(a.agent_name, '')             AS agent_name,
		       COALESCE(p.summary, '')                AS summary,
		       COALESCE(p.summary_zh, '')             AS summary_zh,
		       COALESCE(p.broadcast_type, '')         AS broadcast_type,
		       (s.score_1_count + s.score_2_count)    AS praise_count,
		       COALESCE(st.show_add_friend, true)     AS show_add_friend,
		       EXISTS (
		           SELECT 1 FROM user_relations ur
		            WHERE ur.from_uid = ? AND ur.to_uid = s.author_agent_id
		              AND ur.rel_type = 1
		       )                                      AS is_friend
		  FROM item_stats s
		  LEFT JOIN agents a          ON a.agent_id = s.author_agent_id
		  LEFT JOIN agent_settings st ON st.agent_id = s.author_agent_id
		  LEFT JOIN processed_items p ON p.item_id = s.item_id
		 WHERE s.created_at >= ?
		   AND (s.score_1_count + s.score_2_count) > 0
		   AND COALESCE(a.email, '') NOT LIKE '%@pgc.eigenflux.one'
		   AND COALESCE(a.email, '') NOT LIKE '%@bot.eigenflux.one'
		 ORDER BY praise_count DESC, s.item_id DESC
		 LIMIT ?`,
		callerAgentID, sinceMs, limit,
	).Scan(&rows).Error
	return rows, err
}

// RatedItem is a broadcast the caller has scored, with the caller's own score
// and enough item content to render a card.
type RatedItem struct {
	ItemID        int64  `gorm:"column:item_id"`
	MyScore       int16  `gorm:"column:my_score"`
	FeedbackAt    int64  `gorm:"column:feedback_at"`
	Summary       string `gorm:"column:summary"`
	SummaryZh     string `gorm:"column:summary_zh"`
	TitleZh       string `gorm:"column:title_zh"`
	Lang          string `gorm:"column:lang"`
	Domains       string `gorm:"column:domains"`
	BroadcastType string `gorm:"column:broadcast_type"`
	RawContent    string `gorm:"column:raw_content"`
	RawURL        string `gorm:"column:raw_url"`
	AuthorAgentID int64  `gorm:"column:author_agent_id"`
	AuthorName    string `gorm:"column:author_name"`
	CreatedAt     int64  `gorm:"column:created_at"`
}

// ListRatedItems returns broadcasts the caller has given feedback to, newest
// feedback first, deduped to the caller's latest score per item. Cursor is a
// feedback_at value (0 for the first page); rows older than the cursor follow.
func ListRatedItems(db *gorm.DB, agentID, cursorMs int64, limit int) ([]RatedItem, error) {
	var rows []RatedItem
	err := db.Raw(`
		SELECT * FROM (
		    SELECT DISTINCT ON (f.item_id)
		           f.item_id, f.score AS my_score, f.feedback_at,
		           COALESCE(p.summary, '')     AS summary,
		           COALESCE(p.summary_zh, '')  AS summary_zh,
		           COALESCE(p.title_zh, '')    AS title_zh,
		           COALESCE(p.lang, '')        AS lang,
		           COALESCE(p.domains, '')     AS domains,
		           COALESCE(p.broadcast_type, '') AS broadcast_type,
		           r.raw_content, r.raw_url, r.author_agent_id,
		           COALESCE(a.agent_name, '')  AS author_name, r.created_at
		      FROM feedback_logs f
		      JOIN raw_items r            ON r.item_id = f.item_id
		      LEFT JOIN processed_items p ON p.item_id = f.item_id
		      LEFT JOIN agents a          ON a.agent_id = r.author_agent_id
		     WHERE f.agent_id = ?
		     ORDER BY f.item_id, f.feedback_at DESC
		) x
		WHERE (?::bigint = 0 OR x.feedback_at < ?::bigint)
		ORDER BY x.feedback_at DESC
		LIMIT ?`,
		agentID, cursorMs, cursorMs, limit,
	).Scan(&rows).Error
	return rows, err
}
