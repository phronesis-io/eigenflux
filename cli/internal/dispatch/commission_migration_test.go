package dispatch

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestCommissionMigrationTombstoneDoesNotBlockOtherOrders(t *testing.T) {
	j := commissionJournal(t)
	mustAddCommission(t, j, commissionNotification(901, 1, "seller", false))
	job, _, err := j.NextKind("commission_order")
	if err != nil {
		t.Fatal(err)
	}
	if err = j.Update(job.ID, "completed", "commission_order_not_actionable", "", ""); err != nil {
		t.Fatal(err)
	}
	if len(j.state.Jobs[0].Data) != 0 {
		t.Fatal("expected compacted terminal data")
	}
	j = mustJournal(t, j.binding)
	if j.HasRecordedCommissionOrder("902") {
		t.Fatal("old tombstone blocks unrelated order")
	}
	added, err := j.RecoverUnrecordedCommission("902", 3)
	if err != nil || !added {
		t.Fatalf("added=%v err=%v", added, err)
	}
	recovered, ok, err := j.NextKind("commission_order")
	if err != nil || !ok || recovered.Code != "notification_migration" {
		t.Fatalf("job=%+v err=%v", recovered, err)
	}
	key, err := parseCommissionIntakeData(recovered.Data, j.binding.AgentID)
	if err != nil || key.OrderID != "902" {
		t.Fatal(key, err)
	}
}

func TestCommissionMigrationAtomicDeduplication(t *testing.T) {
	j := commissionJournal(t)
	var count atomic.Int32
	var group sync.WaitGroup
	for i := 0; i < 20; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			added, err := j.RecoverUnrecordedCommission("901", 3)
			if err != nil {
				t.Error(err)
			}
			if added {
				count.Add(1)
			}
		}()
	}
	group.Wait()
	if count.Load() != 1 || len(j.Snapshot()) != 1 {
		t.Fatalf("added=%d jobs=%d", count.Load(), len(j.Snapshot()))
	}
	for _, status := range []string{"unknown", "failed"} {
		t.Run(status, func(t *testing.T) {
			prior := commissionJournal(t)
			if _, err := prior.RecoverCommission("901", 2); err != nil {
				t.Fatal(err)
			}
			job, ok, err := prior.NextKind("commission_order")
			if !ok || err != nil {
				t.Fatal("missing work", err)
			}
			if err = prior.Update(job.ID, status, "existing_outcome", "", ""); err != nil {
				t.Fatal(err)
			}
			added, err := prior.RecoverUnrecordedCommission("901", 4)
			if err != nil || added {
				t.Fatalf("replayed %s: added=%v err=%v", status, added, err)
			}
		})
	}

}

func TestCommissionMigrationPreservesRecordedDecisionWithoutData(t *testing.T) {
	j := commissionJournal(t)
	j.state.Jobs = []Job{{Kind: "commission_order", Status: "completed", CommissionResult: &CommissionIntakeDecision{OrderID: "901"}}}
	if !j.HasRecordedCommissionOrder("901") || j.HasRecordedCommissionOrder("902") {
		t.Fatal("decision identity not preserved")
	}
	added, err := j.RecoverUnrecordedCommission("901", 4)
	if err != nil || added {
		t.Fatal("replayed decision", err)
	}
}
