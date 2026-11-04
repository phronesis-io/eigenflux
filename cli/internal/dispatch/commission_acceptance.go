package dispatch

import "errors"

// ReadCommissionReadiness is read-only even while the account watch is active.
// It never recovers interrupted jobs or writes the owner's journal.
func ReadCommissionReadiness(b Binding, orderID string, version int64) (Job, bool, error) {
	state, _, err := readJournal(b)
	if err != nil {
		return Job{}, false, err
	}
	var blocked bool
	for _, job := range state.Jobs {
		if job.Kind != "commission_order" {
			continue
		}
		if ready := job.CommissionReadiness; ready != nil && ready.Result.OrderID == orderID && ready.Result.OrderVersion == version {
			return cloneJob(job), true, nil
		}
		key, parseErr := parseCommissionIntakeData(job.Data, b.AgentID)
		if parseErr == nil && key.OrderID == orderID && (job.Status == "unknown" || job.Status == "running" || job.Status == "sending") {
			blocked = true
		}
	}
	if blocked {
		return Job{}, false, errors.New("commission_intake_unresolved_use_job_recovery")
	}
	return Job{}, false, nil
}

// ClaimCommissionAcceptance claims only the requested seller order/version.
// Existing failed, uncertain or completed jobs are never implicitly replayed.
// The caller must hold the existing account watch lock before opening this journal.
func (j *Journal) ClaimCommissionAcceptance(orderID string, version int64) (Job, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	id, err := j.recoverCommissionLocked(orderID, version, "operator_recovery", "operator_retry")
	if err != nil {
		return Job{}, err
	}
	for i, job := range j.state.Jobs {
		if job.ID != id {
			continue
		}
		if job.Status != "pending" {
			return Job{}, errors.New("commission_intake_not_pending: inspect the existing job before retrying")
		}
		next := cloneJournal(j.state)
		next.Jobs[i].Status = "running"
		if err := j.save(next); err != nil {
			return Job{}, err
		}
		return cloneJob(j.state.Jobs[i]), nil
	}
	return Job{}, errors.New("job_not_found")
}
