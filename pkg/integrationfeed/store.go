// Package integrationfeed provides durable, server-clock enterprise Feed
// admission. It is independent of personal CLI cadence and volatile page caches.
package integrationfeed

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"eigenflux_server/pkg/integrationidentity"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type Batch struct {
	AgentID         int64  `gorm:"primaryKey" json:"agent_id,string"`
	Sequence        int64  `json:"sequence"`
	BatchID         string `json:"batch_id"`
	RequestHash     string `json:"-"`
	Status          string `json:"status"`
	MatchingVersion int64  `json:"matching_version"`
	Payload         string `gorm:"type:jsonb" json:"-"`
	ItemCount       int    `json:"item_count"`
	LeaseUntil      int64  `json:"-"`
	CreatedAt       int64  `json:"created_at"`
	CompletedAt     int64  `json:"completed_at"`
	NextPollAt      int64  `json:"next_poll_at"`
	FailureCode     string `json:"failure_code,omitempty"`
}

func (Batch) TableName() string { return "integration_feed_batches" }

type Store struct{ DB *gorm.DB }
type Request struct {
	Sequence int64 `json:"sequence"`
	Limit    int   `json:"limit,omitempty"`
}
type RetryError struct {
	Status  int
	Code    string
	Seconds int
}

func (e *RetryError) Error() string { return e.Code }
func (e *RetryError) Unwrap() error {
	return &integrationidentity.Error{Status: e.Status, Code: e.Code}
}
func (e *RetryError) RetryAfter() int { return e.Seconds }
func failure(status int, code string) error {
	return &integrationidentity.Error{Status: status, Code: code}
}
func classify(err error) error {
	if err == nil {
		return nil
	}
	var typed *integrationidentity.Error
	if errors.As(err, &typed) {
		return err
	}
	return failure(503, "INTEGRATION_FEED_UNAVAILABLE")
}
func (s *Store) database(ctx context.Context) *gorm.DB {
	return s.DB.WithContext(ctx).Session(&gorm.Session{Logger: s.DB.Logger.LogMode(logger.Silent)})
}
func now(db *gorm.DB) (int64, error) {
	query := "SELECT (extract(epoch FROM clock_timestamp()) * 1000)::bigint"
	if db.Dialector.Name() == "sqlite" {
		query = "SELECT CAST(strftime('%s','now') AS INTEGER) * 1000"
	}
	var value int64
	err := db.Raw(query).Scan(&value).Error
	return value, err
}

// Begin commits the quota/cadence decision before any external generation.
// Only the caller receiving admitted=true may invoke the Feed RPC once.
func (s *Store) Begin(ctx context.Context, id int64, request Request) (Batch, bool, error) {
	var result Batch
	admitted := false
	if request.Sequence < 1 || request.Limit < 0 || request.Limit > 20 {
		return result, false, failure(400, "INVALID_INTEGRATION_FEED_REQUEST")
	}
	if request.Limit == 0 {
		request.Limit = 20
	}
	if s.DB == nil {
		return result, false, failure(503, "INTEGRATION_FEED_UNAVAILABLE")
	}
	raw, _ := json.Marshal(request)
	digest := sha256.Sum256(raw)
	hash := hex.EncodeToString(digest[:])
	err := s.database(ctx).Transaction(func(tx *gorm.DB) error {
		if err := integrationidentity.LockManagedAgent(tx, id); err != nil {
			return err
		}
		time, err := now(tx)
		if err != nil {
			return err
		}
		var current Batch
		err = tx.First(&current, "agent_id = ?", id).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if current.Sequence > 0 {
			if request.Sequence < current.Sequence {
				return failure(409, "FEED_SEQUENCE_STALE")
			}
			if request.Sequence == current.Sequence {
				if hash != current.RequestHash {
					return failure(409, "FEED_REQUEST_CONFLICT")
				}
				result = current
				return nil
			}
			if current.Status == "generating" && current.LeaseUntil > time {
				return &RetryError{409, "FEED_GENERATION_IN_PROGRESS", 1}
			}
		}
		if request.Sequence != current.Sequence+1 {
			return failure(409, "FEED_SEQUENCE_GAP")
		}
		var managed integrationidentity.Agent
		if err := tx.First(&managed, "agent_id = ?", id).Error; err != nil {
			return err
		}
		if managed.NextPollAt > time {
			return &RetryError{429, "FEED_POLL_TOO_EARLY", int((managed.NextPollAt - time + 999) / 1000)}
		}
		var app struct {
			AppID                  string
			MinPollIntervalSeconds int
			MaxFeedBatchesPerDay   int
			FeedUsageDay           int64
			FeedBatchesToday       int64
		}
		if err := tx.Table("integration_apps").Where("app_id = ?", managed.AppID).First(&app).Error; err != nil {
			return err
		}
		day := time / 86400000
		used := app.FeedBatchesToday
		if app.FeedUsageDay != day {
			used = 0
		}
		if app.MaxFeedBatchesPerDay < 1 {
			return failure(503, "INTEGRATION_FEED_POLICY_INVALID")
		}
		if used >= int64(app.MaxFeedBatchesPerDay) {
			return &RetryError{429, "APPLICATION_FEED_QUOTA_EXCEEDED", int(((day+1)*86400000 - time + 999) / 1000)}
		}
		var projection struct{ SourceRevision int64 }
		if err := tx.Table("integration_matching_profiles").Select("source_revision").Where("agent_id = ?", id).First(&projection).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return failure(409, "CONTEXT_MATCHING_NOT_READY")
			}
			return err
		}
		interval := managed.RequestedPollIntervalSeconds
		if interval < app.MinPollIntervalSeconds {
			interval = app.MinPollIntervalSeconds
		}
		if interval < 10 {
			return failure(503, "INTEGRATION_FEED_POLICY_INVALID")
		}
		result = Batch{AgentID: id, Sequence: request.Sequence, BatchID: uuid.NewString(), RequestHash: hash, Status: "generating", MatchingVersion: projection.SourceRevision, Payload: "{}", LeaseUntil: time + 120000, CreatedAt: time, NextPollAt: time + int64(interval)*1000}
		if current.Sequence == 0 {
			err = tx.Create(&result).Error
		} else {
			err = tx.Model(&Batch{}).Where("agent_id = ?", id).Select("*").Updates(&result).Error
		}
		if err != nil {
			return err
		}
		if err := tx.Model(&integrationidentity.Agent{}).Where("agent_id = ?", id).Update("next_poll_at", result.NextPollAt).Error; err != nil {
			return err
		}
		if err := tx.Table("integration_apps").Where("app_id = ?", app.AppID).Updates(map[string]any{"feed_usage_day": day, "feed_batches_today": used + 1, "feed_batches_total": gorm.Expr("feed_batches_total + 1")}).Error; err != nil {
			return err
		}
		admitted = true
		return nil
	})
	if err != nil {
		return Batch{}, false, classify(err)
	}
	if !admitted {
		if result.Status == "generating" {
			time, err := now(s.database(ctx))
			if err != nil {
				return Batch{}, false, classify(err)
			}
			if result.LeaseUntil <= time {
				if err := s.Unknown(ctx, result); err != nil {
					return Batch{}, false, err
				}
				result.Status = "unknown"
				result.FailureCode = "FEED_GENERATION_UNKNOWN"
			} else {
				return result, false, &RetryError{409, "FEED_GENERATION_IN_PROGRESS", 1}
			}
		}
		if result.Status == "unknown" {
			return result, false, failure(409, "FEED_GENERATION_UNKNOWN")
		}
	}
	return result, admitted, nil
}

func (s *Store) Complete(ctx context.Context, batch Batch, payload json.RawMessage, itemCount int) (bool, error) {
	if s.DB == nil {
		return false, failure(503, "INTEGRATION_FEED_UNAVAILABLE")
	}
	if !json.Valid(payload) || len(payload) > 256<<10 || itemCount < 0 || itemCount > 20 {
		return false, failure(503, "INVALID_FEED_SOURCE_PAYLOAD")
	}
	applied := false
	err := s.database(ctx).Transaction(func(tx *gorm.DB) error {
		if err := integrationidentity.LockManagedAgent(tx, batch.AgentID); err != nil {
			return err
		}
		time, err := now(tx)
		if err != nil {
			return err
		}
		result := tx.Model(&Batch{}).Where("agent_id = ? AND sequence = ? AND batch_id = ? AND status = 'generating' AND lease_until > ?", batch.AgentID, batch.Sequence, batch.BatchID, time).Updates(map[string]any{"status": "ready", "payload": string(payload), "item_count": itemCount, "completed_at": time, "failure_code": ""})
		applied = result.RowsAffected == 1
		return result.Error
	})
	return applied, classify(err)
}

// Unknown is conservative after an RPC error or a lost worker: the same
// sequence must never call the generator again, even after its lease expires.
func (s *Store) Unknown(ctx context.Context, batch Batch) error {
	if s.DB == nil {
		return failure(503, "INTEGRATION_FEED_UNAVAILABLE")
	}
	return classify(s.database(ctx).Model(&Batch{}).Where("agent_id = ? AND batch_id = ? AND status = 'generating'", batch.AgentID, batch.BatchID).Updates(map[string]any{"status": "unknown", "failure_code": "FEED_GENERATION_UNKNOWN"}).Error)
}

func (s *Store) Latest(ctx context.Context, id int64) (Batch, error) {
	var batch Batch
	if s.DB == nil {
		return batch, failure(503, "INTEGRATION_FEED_UNAVAILABLE")
	}
	err := s.database(ctx).Transaction(func(tx *gorm.DB) error {
		if err := integrationidentity.LockManagedAgent(tx, id); err != nil {
			return err
		}
		err := tx.First(&batch, "agent_id = ?", id).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			batch = Batch{AgentID: id, Status: "none"}
			return nil
		}
		if err != nil {
			return err
		}
		time, err := now(tx)
		if err != nil {
			return err
		}
		if batch.Status == "generating" && batch.LeaseUntil <= time {
			batch.Status = "unknown"
			batch.FailureCode = "FEED_GENERATION_UNKNOWN"
			return tx.Model(&batch).Updates(map[string]any{"status": batch.Status, "failure_code": batch.FailureCode}).Error
		}
		return nil
	})
	return batch, classify(err)
}
