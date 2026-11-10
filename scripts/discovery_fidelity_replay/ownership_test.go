package main

import (
	"eigenflux_server/rpc/sort/discovery"
	"testing"
)

func TestOwnershipMetadataIncludesChangedLanguageAndExpiry(t *testing.T) {
	d := discovery.Document{Ref: discovery.SourceRef{Type: discovery.Broadcast, ID: 3}, AuthorID: 7, Visible: true, Active: true, ExpiresAt: 1}
	c := discovery.Context{ID: 1, OwnerID: 2, State: "active", Kinds: []discovery.Kind{discovery.Broadcast}}
	c.Filters.Lang = []string{"zh"}
	d.Slots.Lang = []string{"en"}
	s := &snapshot{At: 100, Inventory: storeDocs([]discovery.Document{d}), Contexts: []discovery.Context{c}, Classes: map[int64]string{3: "ugc"}, Seen: map[string]bool{}}
	if !ownershipCandidates([]*snapshot{s})[3] {
		t.Fatal("model-derived expiry/language excluded a recoverable candidate")
	}
	s.Seen[d.Ref.Key()] = true
	if ownershipCandidates([]*snapshot{s})[3] {
		t.Fatal("consumed candidate included")
	}
	delete(s.Seen, d.Ref.Key())
	s.Inventory[0].Blocked = true
	if ownershipCandidates([]*snapshot{s})[3] {
		t.Fatal("blocked candidate included")
	}
}

func TestOwnershipWorkersAreBounded(t *testing.T) {
	for _, value := range []string{"0", "17", "-1", "bad"} {
		if _, err := ownershipWorkers(value); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
	for _, value := range []string{"", "1", "12", "16"} {
		if n, err := ownershipWorkers(value); err != nil || n < 1 || n > 16 {
			t.Fatalf("rejected %q", value)
		}
	}
}

func TestOwnershipCounterfactualKeepsPGCAndRejectsInvalidUGC(t *testing.T) {
	for _, tc := range []struct {
		class, notes, want string
		drop               bool
	}{
		{"pgc", `{"summary":123}`, "original", false},
		{"ugc", `{"summary":"  FTC  "}`, "  FTC  ", false},
		{"ugc", `{}`, "", false},
		{"ugc", `{"summary":null}`, "", false},
		{"ugc", `{"summary":123}`, "", true},
	} {
		got, drop := ownershipProjection(itemEvidence{Notes: tc.notes, Summary: "original"}, tc.class)
		if got != tc.want || drop != tc.drop {
			t.Fatalf("%+v: got %q,%v", tc, got, drop)
		}
	}
}

func TestOwnershipGroupRejectsStaleSourceAndModelType(t *testing.T) {
	item := itemEvidence{ItemID: 10, Content: "body"}
	d := discovery.Document{Version: "source-v1"}
	row := ownershipGrouping{InputHash: auditInputHash(item), SourceVersion: d.Version, Kind: "info", Policy: "ugc-grouping-v1-f32", GroupID: 3}
	if got, err := ownershipGroup(row, item, d, "info"); err != nil || got != 3 {
		t.Fatal(got, err)
	}
	if _, err := ownershipGroup(row, item, d, "supply"); err == nil {
		t.Fatal("accepted wrong type")
	}
	item.Content = "edited"
	if _, err := ownershipGroup(row, item, d, "info"); err == nil {
		t.Fatal("accepted changed body")
	}
}

func TestPairedSensitivityCannotReplaceCapturedBaseline(t *testing.T) {
	t.Setenv("FIDELITY_UGC_PAIRED", "true")
	t.Setenv("FIDELITY_UGC_OWNERSHIP", "true")
	t.Setenv("FIDELITY_UGC_METADATA", "true")
	t.Setenv("FIDELITY_BASELINE_ONLY", "false")
	t.Setenv("FIDELITY_PGC_COUPLED", "false")
	t.Setenv("FIDELITY_SOURCE_CONTEXT_ONLY", "false")
	_, _, variant := replayVariant()
	if variant != "ugc-ownership-paired" {
		t.Fatal(variant)
	}
	t.Setenv("FIDELITY_BASELINE_ONLY", "true")
	defer func() {
		if recover() == nil {
			t.Fatal("paired model outputs accepted as unchanged baseline")
		}
	}()
	replayVariant()
}
