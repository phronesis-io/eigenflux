package cmd

import (
	"encoding/json"
	"testing"

	"cli.eigenflux.ai/internal/controlcontext"
)

func TestHydrateFeedV2ControlContextFromAppliedCache(t *testing.T) {
	t.Setenv("EIGENFLUX_HOME", t.TempDir())
	if err := controlcontext.Save("test", controlcontext.Snapshot{
		Revision: 7,
		Context:  json.RawMessage(`{"network_goal":{"text":"Find collaborators"},"intent_actions":[]}`),
	}); err != nil {
		t.Fatal(err)
	}
	payload, err := hydrateFeedV2ControlContext("test", json.RawMessage(`{
		"schema_version":"feed_batch.v2",
		"control_context_snapshot":null,
		"personalization":{"context_revision":7,"context_delivery":"unchanged"},
		"items":[]
	}`), 7)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Source  string `json:"control_context_source"`
		Context struct {
			NetworkGoal struct {
				Text string `json:"text"`
			} `json:"network_goal"`
		} `json:"control_context_snapshot"`
	}
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatal(err)
	}
	if got.Source != "local_applied_cache" || got.Context.NetworkGoal.Text != "Find collaborators" {
		t.Fatalf("unexpected hydrated payload: %s", payload)
	}
}

func TestHydrateFeedV2ControlContextRejectsRevisionMismatch(t *testing.T) {
	t.Setenv("EIGENFLUX_HOME", t.TempDir())
	if err := controlcontext.Save("test", controlcontext.Snapshot{
		Revision: 6, Context: json.RawMessage(`{"intent_actions":[]}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := hydrateFeedV2ControlContext("test", json.RawMessage(`{
		"schema_version":"feed_batch.v2","control_context_snapshot":null,"items":[]
	}`), 7); err == nil {
		t.Fatal("expected revision mismatch to fail closed")
	}
}
