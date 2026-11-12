package dal

import (
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// FeedbackHealthReport describes a client's trailing observation window.
// These are client-reported counters, not server feedback or delivery facts.
type FeedbackHealthReport struct {
	RepeatPullsSuppressed int64 `json:"repeat_pulls_suppressed"`
	BatchesHanded         int64 `json:"batches_handed"`
	LocalPolls            int64 `json:"local_polls"`
	RehandedItems         int64 `json:"rehanded_items"`
	BacklogExpiredItems   int64 `json:"backlog_expired_items"`
	SurfaceReminders      int64 `json:"surface_reminders"`
	DetailFetches         int64 `json:"detail_fetches"`
	DetailFetchFailures   int64 `json:"detail_fetch_failures"`
	OversizeBatches       int64 `json:"oversize_batches"`
	OverlappingCycles     int64 `json:"overlapping_cycles"`
	BatchBodyBytes        int64 `json:"batch_body_bytes"`
	BatchBodyMaxBytes     int64 `json:"batch_body_max_bytes"`

	WindowMS          int64 `json:"window_ms"`
	Pulls             int64 `json:"pulls"`
	DeliveredItems    int64 `json:"delivered_items"`
	ScoredItems       int64 `json:"scored_items"`
	StatusRuns        int64 `json:"status_runs"`
	FeedbackAttempts  int64 `json:"feedback_attempts"`
	FeedbackFailures  int64 `json:"feedback_failures"`
	QueueDrops        int64 `json:"queue_drops"`
	QueueDepth        int64 `json:"queue_depth"`
	OldestQueuedAgeMS int64 `json:"oldest_queued_age_ms"`
}

func (r *FeedbackHealthReport) Valid() bool {
	if r == nil {
		return true
	}
	if r.WindowMS < 1 || r.WindowMS > 172800000 || r.OldestQueuedAgeMS < 0 || r.OldestQueuedAgeMS > 604800000 {
		return false
	}
	for _, count := range []int64{r.RepeatPullsSuppressed, r.Pulls, r.DeliveredItems, r.ScoredItems, r.StatusRuns, r.FeedbackAttempts, r.QueueDrops, r.QueueDepth, r.BatchesHanded, r.LocalPolls, r.RehandedItems, r.BacklogExpiredItems, r.SurfaceReminders, r.DetailFetches, r.DetailFetchFailures, r.OversizeBatches, r.OverlappingCycles} {
		if count < 0 || count > 1000000 {
			return false
		}
	}
	if r.BatchBodyBytes < 0 || r.BatchBodyBytes > 1<<40 || r.BatchBodyMaxBytes < 0 || r.BatchBodyMaxBytes > 1<<40 || r.BatchBodyMaxBytes > r.BatchBodyBytes {
		return false
	}
	return r.FeedbackFailures >= 0 && r.FeedbackFailures <= r.FeedbackAttempts
}

type FeedbackHealthDaily struct {
	AgentID              int64  `gorm:"primaryKey;autoIncrement:false"`
	Day                  string `gorm:"primaryKey;type:date"`
	ReportedAt           int64
	FeedbackHealthReport `gorm:"embedded"`
	CLIVersion           string `gorm:"size:32;not null;default:''"`
}

func (FeedbackHealthDaily) TableName() string { return "agent_feedback_health_daily" }

var feedbackHealthLocation = time.FixedZone("Asia/Shanghai", 8*3600)

// ReportFeedbackHealth is called inside the settings transaction. A delayed
// report cannot replace the newest snapshot for the same Shanghai date.
func ReportFeedbackHealth(tx *gorm.DB, agentID, reportedAt int64, cliVersion string, report *FeedbackHealthReport) error {
	if report == nil {
		return nil
	}
	version := []rune(cliVersion)
	if len(version) > 32 {
		version = version[:32]
	}
	row := FeedbackHealthDaily{AgentID: agentID, Day: time.UnixMilli(reportedAt).In(feedbackHealthLocation).Format("2006-01-02"), ReportedAt: reportedAt, FeedbackHealthReport: *report, CLIVersion: string(version)}
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "agent_id"}, {Name: "day"}},
		DoUpdates: clause.AssignmentColumns([]string{"repeat_pulls_suppressed", "reported_at", "window_ms", "pulls", "delivered_items", "scored_items", "status_runs", "feedback_attempts", "feedback_failures", "queue_drops", "queue_depth", "oldest_queued_age_ms", "cli_version", "batches_handed", "local_polls", "rehanded_items", "backlog_expired_items", "surface_reminders", "detail_fetches", "detail_fetch_failures", "oversize_batches", "overlapping_cycles", "batch_body_bytes", "batch_body_max_bytes"}),
		Where:     clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "agent_feedback_health_daily.reported_at <= EXCLUDED.reported_at"}}},
	}).Create(&row).Error
}
