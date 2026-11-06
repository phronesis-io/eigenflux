package maintenance

import (
	"testing"
	"time"
)

func TestNeedCaptureReceiptsAreVersionedIsolatedAndMonotonic(t *testing.T) {
	scope := Scope{Home: t.TempDir(), Server: "test", AgentID: "1"}
	run := NeedCaptureRun{ID: NewID(), CreatedAt: time.Now().UnixMilli(), QueryStatus: "ready", Items: []NeedCaptureItem{{IntentID: "42", IntentVersion: 3, Status: "pending"}}}
	if err := SaveNeedCaptureRun(scope, run); err != nil {
		t.Fatal(err)
	}
	if err := RecordNeedCaptureResult(scope, run.ID, "42", 4, 0, "confirmed", "captured", ""); err == nil {
		t.Fatal("accepted unselected version")
	}
	other := scope
	other.AgentID = "2"
	if _, err := ReadNeedCaptureRun(other, run.ID); err == nil {
		t.Fatal("cross-account receipt")
	}
	other = scope
	other.Server = "other"
	if _, err := ReadNeedCaptureRun(other, run.ID); err == nil {
		t.Fatal("cross-server receipt")
	}
	for _, status := range []string{"unconfirmed", "failed", "confirmed", "unconfirmed"} {
		if err := RecordNeedCaptureResult(scope, run.ID, "42", 3, 0, status, "no_need", ""); err != nil {
			t.Fatal(err)
		}
	}
	got, err := ReadNeedCaptureRun(scope, run.ID)
	if err != nil || got.Items[0].Status != "confirmed" || got.Items[0].Outcome != "no_need" {
		t.Fatalf("receipt lost: %+v %v", got, err)
	}
	state, err := Snapshot(scope)
	if err != nil || len(state.Events) != 0 {
		t.Fatal("local Need records entered server event queue")
	}
}

func TestNeedCaptureRetentionIsBoundedAndExpires(t *testing.T) {
	st := State{NeedCaptures: map[string]NeedCaptureRun{}}
	now := time.Now()
	for i := 0; i < 70; i++ {
		id := NewID()
		st.NeedCaptures[id] = NeedCaptureRun{ID: id, CreatedAt: now.Add(time.Duration(i) * time.Millisecond).UnixMilli()}
	}
	old := NewID()
	st.NeedCaptures[old] = NeedCaptureRun{ID: old, CreatedAt: now.Add(-Retention - time.Second).UnixMilli()}
	pruneNeedCaptures(&st, now)
	if len(st.NeedCaptures) != 64 {
		t.Fatalf("retained %d records", len(st.NeedCaptures))
	}
	if _, ok := st.NeedCaptures[old]; ok {
		t.Fatal("expired record retained")
	}
}
