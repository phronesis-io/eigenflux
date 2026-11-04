package integrationprofile

import (
	"context"
	"encoding/json"
	"errors"

	"eigenflux_server/pkg/integrationidentity"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type Store struct{ DB *gorm.DB }
type State struct {
	AgentID                int64  `gorm:"primaryKey" json:"agent_id,string"`
	SourceRevision         int64  `json:"source_revision"`
	Generation             int64  `json:"-"`
	ContentHash            string `json:"content_hash"`
	Payload                string `gorm:"type:jsonb" json:"-"`
	Status                 string `json:"status"`
	ClaimToken             string `json:"-"`
	LeaseUntil             int64  `json:"-"`
	RetryAt                int64  `json:"-"`
	Attempts               int    `json:"-"`
	ProfileVersion         int64  `json:"accepted_profile_version"`
	AppliedMatchingVersion int64  `json:"applied_matching_version"`
	FailureCode            string `json:"failure_code,omitempty"`
	AcceptedAt             int64  `json:"accepted_at"`
	ProcessedAt            int64  `json:"processed_at"`
}

func (State) TableName() string { return "integration_contexts" }

type Job struct {
	State
	Input Normalized
}

func contextError(status int, code string) error {
	return &integrationidentity.Error{Status: status, Code: code}
}
func clock(db *gorm.DB) (int64, error) {
	query := "SELECT (extract(epoch FROM clock_timestamp()) * 1000)::bigint"
	if db.Dialector.Name() == "sqlite" {
		query = "SELECT CAST(strftime('%s','now') AS INTEGER) * 1000"
	}
	var now int64
	err := db.Raw(query).Scan(&now).Error
	return now, err
}
func (s *Store) database(ctx context.Context) *gorm.DB {
	// A context can contain private prose. Never let SQL error traces print it.
	return s.DB.WithContext(ctx).Session(&gorm.Session{Logger: s.DB.Logger.LogMode(logger.Silent)})
}

// lockManaged matches registration's application-first order and rechecks
// current business authorization for asynchronous processing and retries.
func lockManaged(tx *gorm.DB, agentID int64) error {
	return integrationidentity.LockManagedAgent(tx, agentID)
}

// Accept keeps only the latest input, while a separate matching projection
// retains the last successfully applied version. Equal content does not enqueue
// another extraction merely because the caller advanced source_revision.
func (s *Store) Accept(ctx context.Context, agentID int64, input Input) (State, error) {
	var result State
	normalized, err := Normalize(input)
	if err != nil {
		return result, contextError(400, "INVALID_INTEGRATION_CONTEXT")
	}
	if s.DB == nil {
		return result, contextError(503, "INTEGRATION_CONTEXT_UNAVAILABLE")
	}
	err = s.database(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockManaged(tx, agentID); err != nil {
			return err
		}
		now, err := clock(tx)
		if err != nil {
			return err
		}
		var current State
		err = tx.First(&current, "agent_id = ?", agentID).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if current.AgentID != 0 {
			if normalized.SourceRevision < current.SourceRevision {
				return contextError(409, "CONTEXT_REVISION_STALE")
			}
			if normalized.SourceRevision == current.SourceRevision {
				if normalized.ContentHash != current.ContentHash {
					return contextError(409, "CONTEXT_REVISION_CONFLICT")
				}
				result = current
				return nil
			}
		}
		payload, err := json.Marshal(normalized)
		if err != nil {
			return err
		}
		if current.AgentID == 0 {
			current = State{AgentID: agentID, SourceRevision: normalized.SourceRevision, Generation: 1, ContentHash: normalized.ContentHash, Payload: string(payload), Status: "pending", AcceptedAt: now}
			if err := tx.Create(&current).Error; err != nil {
				return err
			}
		} else {
			values := map[string]any{"source_revision": normalized.SourceRevision, "payload": string(payload), "accepted_at": now}
			if normalized.ContentHash != current.ContentHash || current.Status == "failed" {
				values["generation"] = current.Generation + 1
				values["content_hash"] = normalized.ContentHash
				values["status"] = "pending"
				values["claim_token"] = ""
				values["lease_until"] = 0
				values["retry_at"] = 0
				values["attempts"] = 0
				values["failure_code"] = ""
			}
			if err := tx.Model(&current).Updates(values).Error; err != nil {
				return err
			}
		}
		result = current
		return nil
	})
	return result, classified(err)
}

func classified(err error) error {
	if err == nil {
		return nil
	}
	var typed *integrationidentity.Error
	if errors.As(err, &typed) {
		return typed
	}
	return contextError(503, "INTEGRATION_CONTEXT_UNAVAILABLE")
}

func (s *Store) Get(ctx context.Context, agentID int64) (State, error) {
	var state State
	if s.DB == nil {
		return state, contextError(503, "INTEGRATION_CONTEXT_UNAVAILABLE")
	}
	err := s.database(ctx).First(&state, "agent_id = ?", agentID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return state, contextError(404, "CONTEXT_NOT_FOUND")
	}
	return state, classified(err)
}

// Claim obtains a bounded worker lease. A new content generation invalidates
// the previous token. Equal-content revision updates preserve the same lease.
func (s *Store) Claim(ctx context.Context, agentID int64) (*Job, error) {
	if s.DB == nil {
		return nil, contextError(503, "INTEGRATION_CONTEXT_UNAVAILABLE")
	}
	var job *Job
	err := s.database(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockManaged(tx, agentID); err != nil {
			return err
		}
		now, err := clock(tx)
		if err != nil {
			return err
		}
		var state State
		if err := tx.First(&state, "agent_id = ?", agentID).Error; err != nil {
			return err
		}
		if state.Status == "ready" || state.Status == "failed" || state.RetryAt > now || (state.Status == "processing" && state.LeaseUntil > now) {
			return nil
		}
		if state.Attempts >= 3 {
			return tx.Model(&state).Updates(map[string]any{"status": "failed", "failure_code": "PROCESSING_RETRY_LIMIT", "claim_token": "", "lease_until": 0}).Error
		}
		var input Normalized
		if err := json.Unmarshal([]byte(state.Payload), &input); err != nil {
			return err
		}
		token := uuid.NewString()
		if err := tx.Model(&state).Updates(map[string]any{"status": "processing", "claim_token": token, "lease_until": now + 120000, "attempts": state.Attempts + 1}).Error; err != nil {
			return err
		}
		job = &Job{State: state, Input: input}
		return nil
	})
	return job, classified(err)
}

// Commit runs a profile writer only while its input, worker lease, and managed
// authorization are still current. Stale model results cannot execute writer.
func (s *Store) Commit(ctx context.Context, job *Job, writer func(*gorm.DB, int64) (int64, error)) (bool, error) {
	if s.DB == nil {
		return false, contextError(503, "INTEGRATION_CONTEXT_UNAVAILABLE")
	}
	applied := false
	err := s.database(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockManaged(tx, job.AgentID); err != nil {
			return err
		}
		now, err := clock(tx)
		if err != nil {
			return err
		}
		var current State
		if err := tx.First(&current, "agent_id = ?", job.AgentID).Error; err != nil {
			return err
		}
		if current.Generation != job.Generation || current.ContentHash != job.ContentHash || current.ClaimToken != job.ClaimToken || current.Status != "processing" || current.LeaseUntil <= now {
			return nil
		}
		version, err := writer(tx, current.SourceRevision)
		if err != nil {
			return err
		}
		if err := tx.Model(&current).Updates(map[string]any{"status": "ready", "profile_version": version, "applied_matching_version": current.SourceRevision, "processed_at": now, "claim_token": "", "lease_until": 0, "failure_code": ""}).Error; err != nil {
			return err
		}
		applied = true
		return nil
	})
	return applied, classified(err)
}

func (s *Store) Fail(ctx context.Context, job *Job) error {
	if s.DB == nil {
		return contextError(503, "INTEGRATION_CONTEXT_UNAVAILABLE")
	}
	now, err := clock(s.database(ctx))
	if err != nil {
		return classified(err)
	}
	status := "pending"
	if job.Attempts >= 3 {
		status = "failed"
	}
	return classified(s.database(ctx).Model(&State{}).Where("agent_id = ? AND generation = ? AND claim_token = ? AND status = 'processing'", job.AgentID, job.Generation, job.ClaimToken).Updates(map[string]any{"status": status, "retry_at": now + 5000, "lease_until": 0, "claim_token": "", "failure_code": "PROCESSING_FAILED"}).Error)
}
