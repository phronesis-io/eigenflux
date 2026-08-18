package cmd

import (
	"encoding/json"
	"testing"

	"cli.eigenflux.ai/internal/controlcontext"
)

func TestHydrateFeedV2ControlContextFromAppliedCache(t *testing.T) {
	t.Setenv("EIGENFLUX_HOME", t.TempDir())
	if err := controlcontext.Save("test", controlcontext.Snapshot{
		OwnerAgentID: "agent-1",
		Revision:     7,
		Context:      json.RawMessage(`{"context_revision":7,"network_goal":{"text":"Find collaborators"},"intent_actions":[]}`),
	}); err != nil {
		t.Fatal(err)
	}
	payload, revision, err := hydrateFeedV2ControlContext("test", "agent-1", json.RawMessage(`{
		"schema_version":"feed.v2",
		"control_context_snapshot":null,
		"personalization":{"mode":"intent_aligned","context_revision":7,"context_delivery":"unchanged"},
		"items":[]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if revision != 7 {
		t.Fatalf("revision=%d want=7", revision)
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
		OwnerAgentID: "agent-1", Revision: 6, Context: json.RawMessage(`{"context_revision":6,"intent_actions":[]}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := hydrateFeedV2ControlContext("test", "agent-1", json.RawMessage(`{
		"schema_version":"feed.v2","control_context_snapshot":null,
		"personalization":{"mode":"intent_aligned","context_revision":7},"items":[]
	}`)); err == nil {
		t.Fatal("expected revision mismatch to fail closed")
	}
}

func TestHydrateFeedV2ControlContextSavesNewFullRevision(t *testing.T) {
	t.Setenv("EIGENFLUX_HOME", t.TempDir())
	payload, revision, err := hydrateFeedV2ControlContext("test", "agent-1", json.RawMessage(`{
		"schema_version":"feed.v2",
		"control_context_snapshot":{"context_revision":8,"network_goal":{"text":"New goal"}},
		"personalization":{"mode":"intent_aligned","context_revision":8,"context_delivery":"full"},
		"items":[]
	}`))
	if err != nil || revision != 8 || !json.Valid(payload) {
		t.Fatalf("revision=%d payload=%s err=%v", revision, payload, err)
	}
	cached, err := controlcontext.Load("test", "agent-1")
	if err != nil || cached.Revision != 8 {
		t.Fatalf("cached=%#v err=%v", cached, err)
	}
}
