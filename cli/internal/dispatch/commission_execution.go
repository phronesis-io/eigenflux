package dispatch

import (
	"encoding/json"
	"errors"
	"regexp"
)

var executionReceiptPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

func validCommissionExecutionContext(job Job) bool {
	c := job.CommissionExecution
	if c == nil {
		return true
	}
	if c.AcknowledgedRevision < 0 {
		return false
	}
	if c.OrderVersion <= 0 || c.ExpectedRevision < 0 || (c.Reported != "" && !executionReceiptPattern.MatchString(c.Reported)) {
		return false
	}
	if _, err := positiveNotificationInteger(json.RawMessage(`"` + c.OrderID + `"`)); err != nil {
		return false
	}
	return (job.Kind == "commission_order" && c.Stage == "intake") || (job.Kind == "commission_fulfillment" && c.Stage == "fulfillment")
}

// CommissionExecutionContext retains a read-time fence, never order inputs.
// The server owns the revision even after a resolved issue disappears.
type CommissionExecutionContext struct {
	OrderID              string `json:"order_id"`
	OrderVersion         int64  `json:"order_version"`
	ExpectedRevision     int64  `json:"expected_revision"`
	Stage                string `json:"stage"`
	Reported             string `json:"reported,omitempty"`
	MayResolve           bool   `json:"may_resolve,omitempty"`
	AcknowledgedRevision int64  `json:"acknowledged_revision,omitempty"`
}

type CommissionExecutionReport struct {
	OrderID                   string `json:"-"`
	ExpectedVersion           int64  `json:"expected_version"`
	ExpectedExecutionRevision int64  `json:"expected_execution_revision"`
	JobID                     string `json:"job_id"`
	Stage                     string `json:"stage"`
	Status                    string `json:"status"`
	Code                      string `json:"code"`
}

func (r CommissionExecutionReport) Key() string {
	b, _ := json.Marshal(r)
	return journalID(r.OrderID, string(b))
}

func (j *Journal) SetCommissionExecution(id, orderID string, version, revision int64, mayResolve ...bool) error {
	if _, err := positiveNotificationInteger(json.RawMessage(`"` + orderID + `"`)); err != nil || version <= 0 || revision < 0 {
		return errors.New("invalid_commission_execution_context")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	next := cloneJournal(j.state)
	for i := range next.Jobs {
		job := &next.Jobs[i]
		if job.ID != id {
			continue
		}
		if job.Status != "running" || (job.Kind != "commission_order" && job.Kind != "commission_fulfillment") {
			return errors.New("commission_execution_requires_running_order")
		}
		if job.Kind == "commission_order" {
			key, err := parseCommissionIntakeData(job.Data, j.binding.AgentID)
			if err != nil || key.OrderID != orderID || key.RecipientRole != "seller" {
				return errors.New("commission_execution_order_mismatch")
			}
		} else {
			var data CommissionFulfillmentJob
			if json.Unmarshal(job.Data, &data) != nil || data.OrderID != orderID {
				return errors.New("commission_execution_order_mismatch")
			}
		}
		stage := "intake"
		if job.Kind == "commission_fulfillment" {
			stage = "fulfillment"
		}
		job.CommissionExecution = &CommissionExecutionContext{OrderID: orderID, OrderVersion: version, ExpectedRevision: revision, Stage: stage}
		job.CommissionExecution.MayResolve = len(mayResolve) > 0 && mayResolve[0]
		return j.save(next)
	}
	return errors.New("job_not_found")
}

func commissionExecutionReport(job Job) (CommissionExecutionReport, bool) {
	c := job.CommissionExecution
	if c == nil {
		return CommissionExecutionReport{}, false
	}
	r := CommissionExecutionReport{OrderID: c.OrderID, ExpectedVersion: c.OrderVersion, ExpectedExecutionRevision: c.ExpectedRevision, JobID: job.ID, Stage: c.Stage, Status: "blocked"}
	switch job.Status {
	case "needs_user", "failed", "unknown":
		r.Code = CommissionExecutionCode(job.Code, job.Status)
	case "completed":
		if job.Code == "operator_verified" && c.AcknowledgedRevision > c.ExpectedRevision {
			r.ExpectedExecutionRevision = c.AcknowledgedRevision
		} else if !c.MayResolve {
			return r, false
		}
		if job.Code != "commission_intake_ready" && job.Code != "commission_delivery_confirmed" && job.Code != "operator_verified" {
			return r, false
		}
		r.Status = "resolved"
	default:
		return r, false
	}
	return r, c.Reported != r.Key()
}

func (j *Journal) CommissionExecutionReports() []CommissionExecutionReport {
	j.mu.Lock()
	defer j.mu.Unlock()
	result := []CommissionExecutionReport{}
	for _, job := range j.state.Jobs {
		if r, ok := commissionExecutionReport(job); ok {
			// Paid intake hands recovery to the fulfillment lane. An intake clear
			// must not advance the fence while that lane is already using it.
			if r.Status == "resolved" && job.Kind == "commission_order" {
				handedOff := false
				for _, other := range j.state.Jobs {
					if other.Kind != "commission_fulfillment" {
						continue
					}
					var data CommissionFulfillmentJob
					if json.Unmarshal(other.Data, &data) == nil && data.OrderID == r.OrderID {
						handedOff = true
						break
					}
				}
				if handedOff {
					continue
				}
			}
			result = append(result, r)
		}
	}
	return result
}

func (j *Journal) AcknowledgeCommissionExecution(report CommissionExecutionReport, acknowledgedRevision ...int64) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	next := cloneJournal(j.state)
	for i := range next.Jobs {
		job := &next.Jobs[i]
		if job.ID != report.JobID {
			continue
		}
		current, ok := commissionExecutionReport(*job)
		if !ok || current.Key() != report.Key() {
			return nil
		}
		job.CommissionExecution.Reported = report.Key()
		if report.Status == "blocked" && len(acknowledgedRevision) > 0 && acknowledgedRevision[0] == report.ExpectedExecutionRevision+1 {
			job.CommissionExecution.AcknowledgedRevision = acknowledgedRevision[0]
		}
		return j.save(next)
	}
	return nil
}

// Only fixed categories cross the network and notification boundary.
func CommissionExecutionCode(code, status string) string {
	switch code {
	case "commission_skill_missing", "commission_fulfillment_skill_unavailable":
		return "skill_missing"
	case "commission_skill_unreadable":
		return "skill_unreadable"
	case "commission_skill_invalid":
		return "skill_invalid"
	case "commission_contract_invalid":
		return "contract_invalid"
	case "commission_intake_permission_required", "commission_fulfillment_permission_required":
		return "permission_required"
	case "commission_materials_missing", "commission_intake_needs_input", "commission_fulfillment_needs_input":
		return "materials_missing"
	case "commission_materials_invalid":
		return "materials_invalid"
	case "commission_materials_unavailable":
		return "materials_unavailable"
	case "commission_fulfillment_inputs_changed":
		return "inputs_changed"
	case "commission_fulfillment_order_changed":
		return "order_changed"
	case "commission_output_directory_unavailable":
		return "output_unavailable"
	}
	if status == "unknown" {
		return "execution_unconfirmed"
	}
	if status == "failed" {
		return "execution_failed"
	}
	return "execution_blocked"
}
