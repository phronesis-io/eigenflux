package dispatch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func preparedCommissionDelivery(t *testing.T) (*Journal, Job, CommissionFulfillmentResult) {
	t.Helper()
	j, job, result := preparedFulfillment(t)
	retry := CommissionDeliveryRetry{Attempts: 1, BuyerID: "41", SellerID: "42", SnapshotID: "99"}
	if err := j.SaveCommissionCheckpoint(job.ID, result, "generated-once", retry); err != nil {
		t.Fatal(err)
	}
	return j, job, result
}

func deliveryJob(t *testing.T, j *Journal, id string) Job {
	t.Helper()
	for _, job := range j.Snapshot() {
		if job.ID == id {
			return job
		}
	}
	t.Fatal("missing job")
	return Job{}
}

func dueDelivery(t *testing.T, j *Journal, id string) {
	t.Helper()
	next := cloneJournal(j.state)
	for i := range next.Jobs {
		if next.Jobs[i].ID == id {
			next.Jobs[i].CommissionDelivery.NextAttemptAt = time.Now().Add(-time.Second).UnixMilli()
		}
	}
	if err := j.save(next); err != nil {
		t.Fatal(err)
	}
}

func TestCommissionDeliverySchedulePersistsFiveAttempts(t *testing.T) {
	j, job, result := preparedCommissionDelivery(t)
	if err := j.BeginCommissionDelivery(job.ID, result, "generated-once"); err != nil {
		t.Fatal(err)
	}
	delays := []time.Duration{time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute}
	for attempt := 1; attempt <= 5; attempt++ {
		before := time.Now()
		ok, err := j.ScheduleCommissionDeliveryRetry(job.ID, "commission_delivery_upload_unconfirmed", "generated-once", before)
		if err != nil || !ok {
			t.Fatal("retry not persisted", ok, err)
		}
		stored := deliveryJob(t, j, job.ID)
		if stored.CommissionDelivery.Attempts != attempt || stored.CommissionDelivery.LastCode != "commission_delivery_upload_unconfirmed" {
			t.Fatalf("bad retry metadata %+v", stored)
		}
		if attempt == 5 {
			if stored.Status != "unknown" || stored.Code != "commission_delivery_retry_exhausted" || stored.CommissionDelivery.NextAttemptAt != 0 {
				t.Fatalf("bad retry exhaustion %+v", stored)
			}
			if err := j.Reconcile(job.ID, "failed", ""); err != nil {
				t.Fatal(err)
			}
			if err := j.Retry(job.ID); err == nil {
				t.Fatal("manual retry reset five-attempt limit")
			}
			break
		}
		if stored.Status != "pending" || stored.CommissionDelivery.NextAttemptAt != before.Add(delays[attempt-1]).UnixMilli() {
			t.Fatalf("wrong retry delay %+v", stored)
		}
		j = mustJournal(t, j.binding)
		restored := deliveryJob(t, j, job.ID)
		if !reflect.DeepEqual(stored, restored) {
			t.Fatal("restart changed pending delivery")
		}
		if _, ok, err := j.NextKind("commission_fulfillment"); ok || err != nil {
			t.Fatal("future retry claimed early", err)
		}
		dueDelivery(t, j, job.ID)
		claimed := claimFulfillment(t, j)
		if claimed.CommissionDelivery.Attempts != attempt+1 || claimed.CommissionDirectory != result.OutputDirectory || claimed.CommissionFulfillment == nil {
			t.Fatal("claim reset original artifacts or counter")
		}
		if err := j.BeginCommissionDelivery(job.ID, result, "generated-once"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCommissionDeliveryRestartRecoversOnlyNewEvidence(t *testing.T) {
	for _, stage := range []string{"checkpoint", "sending", "legacy_checkpoint", "legacy_sending"} {
		t.Run(stage, func(t *testing.T) {
			var j *Journal
			var job Job
			var result CommissionFulfillmentResult
			if stage == "legacy_checkpoint" || stage == "legacy_sending" {
				j, job, result = preparedFulfillment(t)
				if err := j.SaveCommissionCheckpoint(job.ID, result, "generated-once"); err != nil {
					t.Fatal(err)
				}
			} else {
				j, job, result = preparedCommissionDelivery(t)
			}
			if stage == "sending" || stage == "legacy_sending" {
				if err := j.BeginCommissionDelivery(job.ID, result, "generated-once"); err != nil {
					t.Fatal(err)
				}
			}
			j = mustJournal(t, j.binding)
			stored := deliveryJob(t, j, job.ID)
			if stage == "legacy_checkpoint" || stage == "legacy_sending" {
				if stored.Status != "unknown" || stored.CommissionDelivery != nil {
					t.Fatal("legacy unknown gained automatic replay")
				}
			} else {
				if stored.Status != "pending" || stored.CommissionDelivery.Attempts != 1 || stored.CommissionDelivery.NextAttemptAt <= time.Now().UnixMilli() {
					t.Fatalf("new idempotent evidence lost %+v", stored)
				}
			}
		})
	}
}

func TestCommissionDeliveryPendingDoesNotBlockOtherFulfillment(t *testing.T) {
	j, job, _ := preparedCommissionDelivery(t)
	if ok, err := j.ScheduleCommissionDeliveryRetry(job.ID, "commission_order_unavailable", "", time.Now()); !ok || err != nil {
		t.Fatal(err)
	}
	var payload CommissionFulfillmentJob
	if err := json.Unmarshal(job.Data, &payload); err != nil {
		t.Fatal(err)
	}
	payload.OrderID = "9007199254740994"
	payload.IntakeResult.OrderID = payload.OrderID
	raw, _ := json.Marshal(payload)
	id := commissionFulfillmentID(j.binding, payload.OrderID)
	next := cloneJournal(j.state)
	next.Jobs = append(next.Jobs, j.newHint(id, "commission_fulfillment", raw))
	if err := j.save(next); err != nil {
		t.Fatal(err)
	}
	claimed := claimFulfillment(t, j)
	if claimed.ID != id {
		t.Fatal("future retry blocked other ready fulfillment")
	}
}

func TestCommissionDeliveryAtomicCheckpointAndScheduleRollback(t *testing.T) {
	for _, stage := range []string{"checkpoint", "schedule"} {
		t.Run(stage, func(t *testing.T) {
			j, job, result := preparedFulfillment(t)
			if stage == "schedule" {
				if err := j.SaveCommissionCheckpoint(job.ID, result, "generated-once", CommissionDeliveryRetry{Attempts: 1, BuyerID: "41", SellerID: "42", SnapshotID: "99"}); err != nil {
					t.Fatal(err)
				}
			}
			before := cloneJournal(j.state)
			j.path = filepath.Join(t.TempDir(), "blocked", "journal.json")
			if err := os.WriteFile(filepath.Dir(j.path), []byte("blocked"), 0600); err != nil {
				t.Fatal(err)
			}
			var err error
			if stage == "checkpoint" {
				err = j.SaveCommissionCheckpoint(job.ID, result, "generated-once", CommissionDeliveryRetry{Attempts: 1, BuyerID: "41", SellerID: "42", SnapshotID: "99"})
			} else {
				_, err = j.ScheduleCommissionDeliveryRetry(job.ID, "commission_order_unavailable", "", time.Now())
			}
			if err == nil || !reflect.DeepEqual(before, j.state) {
				t.Fatal("failed atomic persistence changed memory")
			}
			rows, err := ReadJournalStatus(j.binding)
			if err != nil {
				t.Fatal(err)
			}
			for _, stored := range rows {
				if stored.ID == job.ID && stage == "checkpoint" && (stored.CommissionDelivery != nil || stored.CommissionCheckpoint != nil) {
					t.Fatal("partial first checkpoint was persisted")
				}
			}
		})
	}
}

func TestCommissionDeliveryMetadataCorruptionRejected(t *testing.T) {
	for name, change := range map[string]func(*Job){
		"count":         func(j *Job) { j.CommissionDelivery.Attempts = 6 },
		"zero count":    func(j *Job) { j.CommissionDelivery.Attempts = 0 },
		"negative time": func(j *Job) { j.CommissionDelivery.NextAttemptAt = -1 },
		"missing bytes": func(j *Job) { j.CommissionCheckpoint = nil },
		"parties":       func(j *Job) { j.CommissionDelivery.BuyerID = j.CommissionDelivery.SellerID },
	} {
		t.Run(name, func(t *testing.T) {
			j, job, _ := preparedCommissionDelivery(t)
			next := cloneJournal(j.state)
			for i := range next.Jobs {
				if next.Jobs[i].ID == job.ID {
					change(&next.Jobs[i])
				}
			}
			if err := WriteJSON(j.path, next); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadJournalStatus(j.binding); err == nil {
				t.Fatal("invalid replay metadata accepted")
			}
		})
	}
}
