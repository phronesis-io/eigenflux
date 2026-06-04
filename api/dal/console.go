package dal

import (
	"context"
	"fmt"
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
	AutoReplyPM            bool   `gorm:"column:auto_reply_pm;default:true"`
	FeedDeliveryPreference string `gorm:"column:feed_delivery_preference"`
	Mode                   string `gorm:"column:mode"`
	UpdatedAt              int64  `gorm:"column:updated_at;not null"`
}

func (AgentSettings) TableName() string { return "agent_settings" }

// UpdateAgentReported updates only the agent-reported fields (feed_delivery_preference,
// mode) that are non-nil, leaving console-owned fields untouched. Creates the row if absent.
func UpdateAgentReported(db *gorm.DB, agentID int64, feedPref, mode *string, recurringPublish *bool, feedPollInterval *int32, autoReplyPM *bool) error {
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
		vals["feed_poll_interval"] = *feedPollInterval
	}
	if autoReplyPM != nil {
		vals["auto_reply_pm"] = *autoReplyPM
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

// HighlightItem is one row for the Today highlights: a positively-scored item
// that actually flowed through the agent's feed, joined with its content.
type HighlightItem struct {
	ItemID        int64   `gorm:"column:item_id"`
	ImpressionID  string  `gorm:"column:impression_id"`
	Score         int16   `gorm:"column:score"`
	FeedbackAt    int64   `gorm:"column:feedback_at"`
	Summary       string  `gorm:"column:summary"`
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

// GetHighlightsForAgent returns the agent's top positively-scored feed items
// since sinceMs — "today's picks" drawn from what actually reached the agent.
// Read-only: unlike fetching the live feed, this records no impressions.
func GetHighlightsForAgent(db *gorm.DB, agentID, sinceMs int64, limit int) ([]HighlightItem, error) {
	var rows []HighlightItem
	err := db.Raw(`
		SELECT * FROM (
		    SELECT DISTINCT ON (f.item_id)
		           f.item_id, f.impression_id, f.score, f.feedback_at,
		           COALESCE(p.summary, '')       AS summary,
		           COALESCE(p.suggestion, '')    AS suggestion,
		           COALESCE(p.domains, '')       AS domains,
		           COALESCE(p.keywords, '')      AS keywords,
		           p.broadcast_type,
		           COALESCE(p.quality_score, 0)  AS quality_score,
		           r.raw_content, r.raw_url, r.author_agent_id, r.created_at
		      FROM feedback_logs f
		      JOIN processed_items p ON p.item_id = f.item_id
		      JOIN raw_items r       ON r.item_id = f.item_id
		     WHERE f.agent_id = ? AND f.score >= 1 AND f.feedback_at >= ?
		     ORDER BY f.item_id, f.score DESC
		) x
		ORDER BY x.score DESC, x.quality_score DESC, x.feedback_at DESC
		LIMIT ?`,
		agentID, sinceMs, limit,
	).Scan(&rows).Error
	return rows, err
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
