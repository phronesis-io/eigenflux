package maintenance

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestCycleReceiptsRequireEveryStageAndIsolateAccounts(t *testing.T) {
	s := Scope{Home: t.TempDir(), Server: "test", AgentID: "1"}
	env := RuntimeObservation{OS: "darwin", Arch: "arm64", Model: "model-v1"}
	c, err := StartCycle(s, "full", []string{"feed", "communication"}, env)
	if err != nil {
		t.Fatal(err)
	}
	initial, _ := Snapshot(s)
	if len(initial.Events) != 1 || initial.Events[0].Phase != "environment" {
		t.Fatal("plan creation counted as execution")
	}
	if ReportCycle(s, c.ID, "cycle", "failed") == nil {
		t.Fatal("missing evidence counted as failure")
	}
	if ReportCycle(s, c.ID, "cycle", "completed") == nil {
		t.Fatal("unperformed cycle completed")
	}
	if ReportCycle(s, c.ID, "made_up", "completed") == nil {
		t.Fatal("unplanned stage accepted")
	}
	other := s
	other.AgentID = "2"
	if ReportCycle(other, c.ID, "feed", "completed") == nil {
		t.Fatal("cross-account cycle")
	}
	var wg sync.WaitGroup
	for _, stage := range []string{"feed", "communication"} {
		wg.Add(1)
		go func(stage string) {
			defer wg.Done()
			if e := ReportCycle(s, c.ID, stage, "completed"); e != nil {
				t.Error(e)
			}
		}(stage)
	}
	wg.Wait()
	if e := ReportCycle(s, c.ID, "cycle", "completed"); e != nil {
		t.Fatal(e)
	}
	if e := ReportCycle(s, c.ID, "cycle", "completed"); e != nil {
		t.Fatal(e)
	}
	if e := ReportCycle(s, c.ID, "feed", "completed"); e != nil {
		t.Fatal(e)
	}
	st, _ := Snapshot(s)
	if len(st.Events) != 5 || st.Events[1].Result != "started" || st.Events[1].EventAt != st.Events[2].EventAt {
		t.Fatalf("duplicate or lost receipts: %d", len(st.Events))
	}
	if ReportCycle(s, c.ID, "cycle", "failed") == nil {
		t.Fatal("terminal result overwritten")
	}
}
func TestFailedStageCannotBecomeSuccessfulCycle(t *testing.T) {
	s := Scope{Home: t.TempDir(), Server: "test", AgentID: "1"}
	c, _ := StartCycle(s, "full", []string{"feed"}, RuntimeObservation{})
	if e := ReportCycle(s, c.ID, "feed", "failed"); e != nil {
		t.Fatal(e)
	}
	if ReportCycle(s, c.ID, "cycle", "completed") == nil {
		t.Fatal("failure became success")
	}
	if e := ReportCycle(s, c.ID, "cycle", "failed"); e != nil {
		t.Fatal(e)
	}
	update(s, func(st *State) error {
		x := st.Cycles[c.ID]
		x.StartedAt = time.Now().Add(-8 * 24 * time.Hour).UnixMilli()
		st.Cycles[c.ID] = x
		return nil
	})
	if ReportCycle(s, c.ID, "cycle", "failed") == nil {
		t.Fatal("expired cycle accepted")
	}
}
func TestRuntimeObservationRejectsPrivateFreeTextAndForeignMetadata(t *testing.T) {
	e := NewEvent(NewID(), "runtime", "auto", "environment", "observed")
	e.Runtime = &RuntimeObservation{Product: "codex", OS: "darwin", Arch: "arm64", Model: "provider/model"}
	if err := Validate(e); err != nil {
		t.Fatal(err)
	}
	e.OuterLoop = &OuterLoopObservation{}
	if Validate(e) == nil {
		t.Fatal("mixed setup and runtime observation accepted")
	}
	e.OuterLoop = nil
	e.Runtime.Model = "my secret text"
	if Validate(e) == nil {
		t.Fatal("free text accepted")
	}
	e.Runtime.Model = ""
	e.Commission = &Commission{}
	if Validate(e) == nil {
		t.Fatal("mixed metadata accepted")
	}
}

func TestCycleCapacityReclaimsCompletedWithoutDroppingQueuedReceipts(t *testing.T) {
	s := Scope{Home: t.TempDir(), Server: "test", AgentID: "1"}
	err := update(s, func(st *State) error {
		st.Cycles = map[string]Cycle{}
		for i := 0; i < 256; i++ {
			id := fmt.Sprintf("%032d", i)
			st.Cycles[id] = Cycle{ID: id, StartedAt: time.Now().UnixMilli(), Result: "completed"}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	c, err := StartCycle(s, "full", []string{"feed"}, RuntimeObservation{})
	if err != nil {
		t.Fatal(err)
	}
	st, _ := Snapshot(s)
	if len(st.Cycles) != 256 || st.Cycles[c.ID].ID != c.ID || len(st.Events) != 1 {
		t.Fatal("capacity recovery lost state")
	}
}

func TestRuntimeUploadRejectionDoesNotBlockLegacyMaintenance(t *testing.T) {
	s := Scope{Home: t.TempDir(), Server: "test", AgentID: "1"}
	c, err := StartCycle(s, "full", []string{"feed"}, RuntimeObservation{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := Flush(s, now, func(batch []Event) error {
		if len(batch) != 1 || batch[0].Runtime == nil {
			t.Fatal("wrong runtime batch")
		}
		return fmt.Errorf("old API rejects runtime")
	}); err == nil {
		t.Fatal("missing rejection")
	}
	legacy := NewEvent(NewID(), "skills", "auto", "read", "rules_read")
	legacy.ToVersion = "revision"
	if err := Record(s, legacy); err != nil {
		t.Fatal(err)
	}
	if err := Flush(s, now, func(batch []Event) error {
		if len(batch) != 1 || batch[0].EventID != legacy.EventID {
			t.Fatal("runtime mixed into legacy batch")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	st, _ := Snapshot(s)
	if len(st.Events) != 1 || st.Events[0].AttemptID != c.ID || st.RuntimeFailures != 1 || st.Failures != 0 {
		t.Fatalf("lost isolated retry state: %+v", st)
	}
	called := false
	if err := Flush(s, now, func([]Event) error { called = true; return nil }); err != nil || called {
		t.Fatal("runtime backoff ignored")
	}
	if err := Flush(s, now.Add(time.Minute), func(batch []Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	st, _ = Snapshot(s)
	if len(st.Events) != 0 || st.RuntimeFailures != 0 {
		t.Fatal("runtime retry failed")
	}
}

func TestRuntimeOverflowPreservesExistingMaintenance(t *testing.T) {
	s := Scope{Home: t.TempDir(), Server: "test", AgentID: "1"}
	legacy := NewEvent(NewID(), "skills", "auto", "read", "rules_read")
	legacy.ToVersion = "revision"
	err := update(s, func(st *State) error {
		st.Events = append(st.Events, legacy)
		for i := 0; i < MaxEvents; i++ {
			e := NewEvent(NewID(), "runtime", "auto", "environment", "observed")
			e.Runtime = &RuntimeObservation{}
			st.Events = append(st.Events, e)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	st, err := Snapshot(s)
	if err != nil || len(st.Events) != MaxEvents || st.Events[0].EventID != legacy.EventID || st.Dropped != 1 {
		t.Fatal("runtime backlog evicted maintenance")
	}
}

func TestOptionalNeedDoesNotGateFeedCycle(t *testing.T) {
	for _, outcome := range []string{"", "failed"} {
		scope := Scope{Home: t.TempDir(), Server: "test", AgentID: "1"}
		cycle, err := StartCycle(scope, "full", []string{"feed", "need_capture"}, RuntimeObservation{})
		if err != nil {
			t.Fatal(err)
		}
		if outcome != "" {
			if err := ReportCycle(scope, cycle.ID, "need_capture", outcome); err != nil {
				t.Fatal(err)
			}
		}
		if err := ReportCycle(scope, cycle.ID, "cycle", "failed"); err == nil {
			t.Fatal("Need alone failed the cycle")
		}
		if err := ReportCycle(scope, cycle.ID, "feed", "completed"); err != nil {
			t.Fatal(err)
		}
		cycle, err = ReadCycle(scope, cycle.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, missing := CycleMissing(cycle); len(missing) != 0 {
			t.Fatalf("missing stages: %v", missing)
		}
		if err := ReportCycle(scope, cycle.ID, "cycle", "completed"); err != nil {
			t.Fatal(err)
		}
	}
}
