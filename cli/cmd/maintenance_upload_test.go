package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/maintenance"
)

func TestMaintenanceFailureMetadataUploadsToStrictOldGatewayWithoutBlockingQueue(t *testing.T) {
	type oldOuter struct {
		Environment maintenance.RuntimeEnvironment `json:"environment"`
		Scheduler   *maintenance.SchedulerReadback `json:"scheduler,omitempty"`
	}
	type oldEvent struct {
		maintenance.Event
		OuterLoop *oldOuter `json:"outer_loop,omitempty"`
	}
	scope := maintenance.Scope{Home: t.TempDir(), Server: "test", AgentID: "42"}
	failed := maintenance.NewEvent(maintenance.NewID(), "watch", "auto", "skills", "failed")
	failed.ErrorCode = "skills_failed"
	failed.OuterLoop = &maintenance.OuterLoopObservation{Environment: maintenance.DetectRuntimeEnvironment(), FailureReason: "skills_target_unresolved"}
	ordinary := maintenance.NewEvent(maintenance.NewID(), "cli", "auto", "check", "started")
	for _, e := range []maintenance.Event{failed, ordinary} {
		if err := maintenance.Record(scope, e); err != nil {
			t.Fatal(err)
		}
	}
	// Ensure this fixture really rejects the new nested body field.
	raw, _ := json.Marshal(map[string]interface{}{"events": []maintenance.Event{failed}})
	var baseline struct {
		Events []oldEvent `json:"events"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&baseline) == nil {
		t.Fatal("old gateway fixture accepted new body field")
	}
	calls := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/v2/maintenance/events:batch" {
			t.Error(r.URL.Path)
		}
		var reasons map[string]string
		if err := json.Unmarshal([]byte(r.Header.Get(watchFailuresHeader)), &reasons); err != nil || reasons[failed.EventID] != "skills_target_unresolved" || len(reasons) != 1 {
			t.Errorf("wrong metadata: %v %v", reasons, err)
		}
		var body struct {
			Events []oldEvent `json:"events"`
		}
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		if err := d.Decode(&body); err != nil {
			t.Errorf("old gateway rejected body: %v", err)
			http.Error(w, "unknown field", 400)
			return
		}
		if len(body.Events) != 2 {
			t.Errorf("mixed batch lost: %+v", body)
		}
		if failed.OuterLoop.FailureReason != "skills_target_unresolved" {
			t.Error("source event mutated")
		}
		state, err := maintenance.Snapshot(scope)
		if err != nil || len(state.Events) != 2 || state.Events[0].OuterLoop.FailureReason != "skills_target_unresolved" {
			t.Errorf("local journal changed before ACK: %+v %v", state, err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{"accepted_events":2}}`))
	}))
	defer api.Close()
	c := client.New(api.URL+"/api/v2", "token", "test", client.Meta{})
	if err := maintenance.Flush(scope, time.Now(), func(events []maintenance.Event) error { _, err := postMaintenanceEvents(c, events); return err }); err != nil {
		t.Fatal(err)
	}
	state, err := maintenance.Snapshot(scope)
	if err != nil || len(state.Events) != 0 || state.Failures != 0 || state.NextFlush != 0 || calls != 1 {
		t.Fatalf("compatibility upload blocked queue: %+v calls=%d err=%v", state, calls, err)
	}
	if failed.OuterLoop.FailureReason != "skills_target_unresolved" {
		t.Fatal("original detail lost")
	}
}
