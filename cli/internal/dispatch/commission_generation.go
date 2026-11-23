package dispatch

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const CommissionGenerationMaxAttempts = 3

// CommissionGeneration identifies the local attempt before the delivery checkpoint.
// Order ID, version, inspected input fingerprint and directory live on the same job.
type CommissionGeneration struct {
	Attempts      int                   `json:"attempts"`
	Mode          string                `json:"mode"`
	LocalOnly     bool                  `json:"local_only,omitempty"`
	BuyerID       string                `json:"buyer_id"`
	SellerID      string                `json:"seller_id"`
	SnapshotID    string                `json:"snapshot_id"`
	DeliveryDueAt int64                 `json:"delivery_due_at,omitempty"`
	Evidence      GenerationRunEvidence `json:"evidence"`
	NextProbeAt   int64                 `json:"next_probe_at,omitempty"`
}

// RequiresIsolatedRetry protects every attempt following a released prompt.
// An isolated retry whose new prompt was never released still inherits this boundary.
func (g CommissionGeneration) RequiresIsolatedRetry() bool {
	return g.LocalOnly || g.Evidence.PID != 0
}

func commissionGenerationPrepared(job Job) bool {
	return job.Code == "commission_fulfillment_prepared" || (job.Code == "commission_generation_checking" && job.CommissionGeneration != nil)
}

func validCommissionGeneration(job Job, agentID string) bool {
	g := job.CommissionGeneration
	if g == nil {
		return job.Code != "commission_generation_checking" && job.Code != "commission_generation_probe_pending"
	}
	if job.Kind != "commission_fulfillment" || !validCommissionDirectory(job.CommissionDirectory) || job.CommissionCheckpoint != nil || job.CommissionDelivery != nil || job.CommissionFulfillment != nil {
		return false
	}
	if g.Attempts < 1 || g.Attempts > CommissionGenerationMaxAttempts || (g.Mode != "generate" && g.Mode != "verify") || g.DeliveryDueAt < 0 || g.NextProbeAt < 0 {
		return false
	}
	for _, id := range []string{g.BuyerID, g.SellerID, g.SnapshotID} {
		if normalized, err := positiveNotificationInteger(json.RawMessage(id)); err != nil || normalized != id {
			return false
		}
	}
	if g.BuyerID == g.SellerID || g.SellerID != agentID {
		return false
	}
	if (job.Status == "pending") != (job.Code == "commission_generation_probe_pending") || (g.NextProbeAt != 0 && job.Status != "pending") {
		return false
	}
	e := g.Evidence
	if e.PID < 0 || e.PID > 1<<31-1 || !validGenerationSession(e.SessionID) || (e.PID == 0 && (e.SessionID != "" || e.Result != nil)) {
		return false
	}
	if e.Result != nil {
		if e.Result.SessionID != e.SessionID || e.Result.RunID != "" {
			return false
		}
		_, ok := generationReceipt(job, e.Result.Text)
		return ok
	}
	return true
}

func validGenerationSession(id string) bool {
	return len(id) <= 256 && utf8.ValidString(id) && strings.IndexFunc(id, unicode.IsControl) < 0
}

// The only durable model output is the bounded, strictly parsed business receipt.
func generationReceipt(job Job, text string) (string, bool) {
	if len(text) > 64<<10 {
		return "", false
	}
	var queued CommissionFulfillmentJob
	if json.Unmarshal(job.Data, &queued) != nil {
		return "", false
	}
	decision, err := ParseCommissionFulfillmentDecision(text, job.ID, queued.OrderID, queued.OrderVersion)
	if err != nil {
		return "", false
	}
	canonical, err := json.Marshal(decision)
	return string(canonical), err == nil && len(canonical) <= 64<<10
}

// All writes are conditional on the current claim and the absence of a handoff
// to delivery. Failed persistence leaves the in-memory evidence intact.
func (j *Journal) updateGeneration(id string, update func(*Job) error) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	next := cloneJournal(j.state)
	for i := range next.Jobs {
		job := &next.Jobs[i]
		if job.ID != id {
			continue
		}
		if job.Kind != "commission_fulfillment" || job.Status != "running" || job.CommissionDelivery != nil || job.CommissionCheckpoint != nil || job.CommissionFulfillment != nil {
			return errors.New("commission_generation_requires_running_job")
		}
		if err := update(job); err != nil {
			return err
		}
		if !validCommissionGeneration(*job, j.binding.AgentID) {
			return errors.New("invalid_commission_generation")
		}
		return j.saveUpdatedJob(next, i)
	}
	return errors.New("job_not_found")
}

// BeginCommissionGeneration spends one attempt only immediately before invoking
// generation. A claimed evidence probe or read-only verification spends none.
func (j *Journal) BeginCommissionGeneration(id, directory, mode string, identity CommissionGeneration) error {
	return j.updateGeneration(id, func(job *Job) error {
		previous := job.CommissionGeneration
		g := CommissionGeneration{Attempts: 1, Mode: mode, LocalOnly: identity.LocalOnly, BuyerID: identity.BuyerID, SellerID: identity.SellerID, SnapshotID: identity.SnapshotID, DeliveryDueAt: identity.DeliveryDueAt}
		if previous == nil {
			if mode != "generate" || (job.CommissionDirectory != "" && job.CommissionDirectory != directory) {
				return errors.New("commission_generation_not_preparable")
			}
		} else {
			if job.Code != "commission_generation_checking" || previous.Mode != "generate" || (mode == "verify" && directory != job.CommissionDirectory) || g.BuyerID != previous.BuyerID || g.SellerID != previous.SellerID || g.SnapshotID != previous.SnapshotID || g.DeliveryDueAt != previous.DeliveryDueAt {
				return errors.New("commission_generation_attempt_changed")
			}
			g.Attempts = previous.Attempts
			if mode == "verify" {
				g.LocalOnly = previous.LocalOnly
			}
			if mode == "generate" {
				if (previous.RequiresIsolatedRetry() && !identity.LocalOnly) || directory == job.CommissionDirectory || filepath.Dir(directory) != filepath.Dir(job.CommissionDirectory) {
					return errors.New("commission_generation_external_effects_unconfirmed")
				}
				if g.Attempts >= CommissionGenerationMaxAttempts {
					return errors.New("commission_generation_retry_limit_reached")
				}
				g.Attempts++
			}
		}
		job.CommissionDirectory = directory
		job.CommissionGeneration = &g
		job.SessionID = ""
		job.Code = "commission_fulfillment_prepared"
		return nil
	})
}

// ObserveCommissionGeneration accepts cumulative evidence from this exact
// process and attempt. Invalid model output is never written to the journal.
func (j *Journal) ObserveCommissionGeneration(id string, attempt int, mode string, evidence GenerationRunEvidence) error {
	return j.updateGeneration(id, func(job *Job) error {
		g := job.CommissionGeneration
		if g == nil || job.Code != "commission_fulfillment_prepared" || g.Attempts != attempt || g.Mode != mode {
			return errors.New("commission_generation_attempt_changed")
		}
		previous := g.Evidence
		if evidence.PID <= 0 || (previous.PID != 0 && previous.PID != evidence.PID) || (previous.SessionID != "" && previous.SessionID != evidence.SessionID) {
			return errors.New("commission_generation_process_changed")
		}
		if evidence.Result != nil {
			text, ok := generationReceipt(*job, evidence.Result.Text)
			evidence.Result = nil
			if ok {
				evidence.Result = &Result{Text: text, SessionID: evidence.SessionID}
			}
		}
		if previous.Result != nil && (evidence.Result == nil || *previous.Result != *evidence.Result) {
			return errors.New("commission_generation_receipt_changed")
		}
		g.Evidence = evidence
		job.SessionID = evidence.SessionID
		return nil
	})
}

// ScheduleCommissionGenerationProbe releases the serial lane. Order/network and
// process evidence are checked on a later claim, without consuming another run.
func (j *Journal) ScheduleCommissionGenerationProbe(id string, now time.Time) error {
	return j.updateGeneration(id, func(job *Job) error {
		g := job.CommissionGeneration
		if g == nil {
			return errors.New("commission_generation_missing")
		}
		job.Status, job.Code = "pending", "commission_generation_probe_pending"
		g.NextProbeAt = now.Add(time.Minute).UnixMilli()
		if g.DeliveryDueAt > now.UnixMilli() && g.NextProbeAt > g.DeliveryDueAt {
			g.NextProbeAt = g.DeliveryDueAt
		}
		return nil
	})
}
