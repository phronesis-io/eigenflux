package dal

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"eigenflux_server/pkg/mq"

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
	AgentID                int64  `gorm:"column:agent_id;primaryKey"`
	RecurringPublish       bool   `gorm:"column:recurring_publish;default:true"`
	FeedPollInterval       int32  `gorm:"column:feed_poll_interval;default:300"`
	FeedDeliveryPreference string `gorm:"column:feed_delivery_preference"`
	Mode                   string `gorm:"column:mode"`
	UpdatedAt              int64  `gorm:"column:updated_at;not null"`
}

func (AgentSettings) TableName() string { return "agent_settings" }

// UpdateAgentReported updates only the agent-reported fields (feed_delivery_preference,
// mode) that are non-nil, leaving console-owned fields untouched. Creates the row if absent.
func UpdateAgentReported(db *gorm.DB, agentID int64, feedPref, mode *string) error {
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
	return db.Model(&AgentSettings{}).Where("agent_id = ?", agentID).Updates(vals).Error
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

func CountActivityByDate(db *gorm.DB, agentID int64, sinceMs int64) ([]DateCount, error) {
	var results []DateCount
	err := db.Raw(
		`SELECT to_char(to_timestamp(created_at / 1000.0), 'YYYY-MM-DD') AS date, COUNT(*) AS count
		 FROM agent_activity_log
		 WHERE agent_id = ? AND created_at >= ?
		 GROUP BY date
		 ORDER BY date`,
		agentID, sinceMs,
	).Scan(&results).Error
	return results, err
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
	now := time.Now().UnixMilli()
	// Use a map to avoid GORM's default tag overriding zero values (e.g. false → true).
	vals := map[string]interface{}{
		"recurring_publish":  settings.RecurringPublish,
		"feed_poll_interval": settings.FeedPollInterval,
		"updated_at":         now,
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
// set is its keywords ∪ domains (split, trim, lower); a beat keyword matches
// an item when it is in that set — the same lowercase exact-overlap notion the
// feed recall uses.

// BeatItemTags is one item's topic tags (comma-separated columns).
type BeatItemTags struct {
	Keywords string `gorm:"column:keywords"`
	Domains  string `gorm:"column:domains"`
}

// TagSet returns the item's deduplicated lowercase tag set.
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

// CountBeatMatches returns, per beat keyword (already lowercased), how many
// rows contain it in their tag set.
func CountBeatMatches(rows []BeatItemTags, beats []string) map[string]int64 {
	counts := make(map[string]int64, len(beats))
	for _, row := range rows {
		set := row.TagSet()
		for _, beat := range beats {
			if _, ok := set[beat]; ok {
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
