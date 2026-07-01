package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildFeedEvents_StampsDeterministicDedupKey(t *testing.T) {
	in := `[{"item_id":"123","kind":"surface","impression_id":"imp_456"}]`
	events, err := buildFeedEvents(in, "agent-1")
	if err != nil {
		t.Fatalf("buildFeedEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len = %d, want 1", len(events))
	}
	dk, _ := events[0]["dedup_key"].(string)
	if len(dk) != 32 {
		t.Fatalf("dedup_key = %q (len %d), want 32 chars", dk, len(dk))
	}
	// Same identity + scope is idempotent.
	again, _ := buildFeedEvents(in, "agent-1")
	if again[0]["dedup_key"] != dk {
		t.Fatalf("dedup_key not stable: %v != %v", again[0]["dedup_key"], dk)
	}
	// Different agent scope yields a different token (global unique index safety).
	other, _ := buildFeedEvents(in, "agent-2")
	if other[0]["dedup_key"] == dk {
		t.Fatalf("dedup_key collided across agent scopes")
	}
}

func TestBuildFeedEvents_KindDifferentiatesKey(t *testing.T) {
	base := `[{"item_id":"9","kind":"surface","impression_id":"imp_1"}]`
	other := `[{"item_id":"9","kind":"question","impression_id":"imp_1"}]`
	a, _ := buildFeedEvents(base, "s")
	b, _ := buildFeedEvents(other, "s")
	if a[0]["dedup_key"] == b[0]["dedup_key"] {
		t.Fatal("different kinds must produce different dedup_key")
	}
}

func TestBuildFeedEvents_AcceptsNumericItemID(t *testing.T) {
	events, err := buildFeedEvents(`[{"item_id":123,"kind":"task","impression_id":"i"}]`, "s")
	if err != nil {
		t.Fatalf("numeric item_id rejected: %v", err)
	}
	if events[0]["item_id"] != "123" {
		t.Fatalf("item_id = %v, want \"123\"", events[0]["item_id"])
	}
}

func TestBuildFeedEvents_ExplicitDedupKeyWins(t *testing.T) {
	events, err := buildFeedEvents(`[{"item_id":"1","kind":"surface","dedup_key":"manual-key"}]`, "s")
	if err != nil {
		t.Fatalf("buildFeedEvents: %v", err)
	}
	if events[0]["dedup_key"] != "manual-key" {
		t.Fatalf("explicit dedup_key overridden: %v", events[0]["dedup_key"])
	}
}

func TestBuildFeedEvents_PassesThroughOptionalFields(t *testing.T) {
	events, err := buildFeedEvents(`[{"item_id":"1","kind":"surface","impression_id":"i","brief":"b","channel":"lark","ts":1700000000000}]`, "s")
	if err != nil {
		t.Fatalf("buildFeedEvents: %v", err)
	}
	ev := events[0]
	if ev["brief"] != "b" || ev["channel"] != "lark" {
		t.Fatalf("optional fields not passed through: %v", ev)
	}
	if _, ok := ev["ts"]; !ok {
		t.Fatal("ts not passed through")
	}
}

func TestBuildFeedEvents_Errors(t *testing.T) {
	cases := map[string]string{
		"invalid json": `not-json`,
		"empty array":  `[]`,
		"missing kind": `[{"item_id":"1"}]`,
		"bad kind":     `[{"item_id":"1","kind":"bogus"}]`,
		"missing item": `[{"kind":"surface"}]`,
	}
	for name, in := range cases {
		if _, err := buildFeedEvents(in, "s"); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}

func TestBuildFeedEvents_BatchCap(t *testing.T) {
	// 51 events exceeds the cap.
	s := "["
	for i := 0; i < 51; i++ {
		if i > 0 {
			s += ","
		}
		s += `{"item_id":"1","kind":"surface","impression_id":"i"}`
	}
	s += "]"
	if _, err := buildFeedEvents(s, "scope"); err == nil {
		t.Fatal("expected cap error for 51 events")
	}
}

// parseFeedEventItems must read the plugin's batch file, which wraps events in
// {"events":[...]} — the shape written by feedback-queue.ts before invoking
// `feed event push --batch <file>`.
func TestParseFeedEventItems_BatchFileWrapper(t *testing.T) {
	dir := t.TempDir()
	batch := filepath.Join(dir, "batch.json")
	contents := `{"events":[{"item_id":"1","kind":"surface","impression_id":"imp_1","dedup_key":"plugin-key"},{"item_id":"2","kind":"task","impression_id":"imp_2","dedup_key":"k2"}]}`
	if err := os.WriteFile(batch, []byte(contents), 0o600); err != nil {
		t.Fatalf("write batch: %v", err)
	}
	items, err := parseFeedEventItems("", batch)
	if err != nil {
		t.Fatalf("parseFeedEventItems: %v", err)
	}
	events, err := buildFeedEventsFromItems(items, "scope")
	if err != nil {
		t.Fatalf("buildFeedEventsFromItems: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("len = %d, want 2", len(events))
	}
	// Plugin-computed dedup_key must survive so retried batches stay idempotent.
	if events[0]["dedup_key"] != "plugin-key" {
		t.Fatalf("dedup_key = %v, want plugin-key", events[0]["dedup_key"])
	}
}

func TestParseFeedEventItems_InlineArray(t *testing.T) {
	items, err := parseFeedEventItems(`[{"item_id":"1","kind":"surface"}]`, "")
	if err != nil {
		t.Fatalf("parseFeedEventItems: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len = %d, want 1", len(items))
	}
}

func TestParseFeedEventItems_MissingBatchFile(t *testing.T) {
	if _, err := parseFeedEventItems("", filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Fatal("expected error for missing batch file")
	}
}

func TestParseFeedEventItems_InvalidBatchJSON(t *testing.T) {
	dir := t.TempDir()
	batch := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(batch, []byte("not-json"), 0o600); err != nil {
		t.Fatalf("write batch: %v", err)
	}
	if _, err := parseFeedEventItems("", batch); err == nil {
		t.Fatal("expected error for invalid batch JSON")
	}
}
