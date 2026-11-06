package generator

import (
	"math/rand"
	"testing"
)

func sameFingerprints(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestNewFingerprintWindowEmpty(t *testing.T) {
	for name, history := range map[string][]string{
		"nil":       nil,
		"empty":     {},
		"all empty": {"", "", ""},
	} {
		w := NewFingerprintWindow(history)
		if got := w.Slice(); len(got) != 0 {
			t.Errorf("%s: empty window has %d entries: %v", name, len(got), got)
		}
		if w.Contains("anything") {
			t.Errorf("%s: empty window should not contain anything", name)
		}
	}
}

func TestFingerprintWindowTrimsToLastFive(t *testing.T) {
	w := NewFingerprintWindow([]string{"a", "b", "c", "d", "e", "f", "g"})
	want := []string{"c", "d", "e", "f", "g"}
	if got := w.Slice(); !sameFingerprints(got, want) {
		t.Errorf("trim = %v, want the last five oldest->newest %v", got, want)
	}
}

func TestFingerprintWindowDeduplicatesAndDropsEmpty(t *testing.T) {
	w := NewFingerprintWindow([]string{"a", "", "a", "b", "c"})
	want := []string{"a", "b", "c"}
	if got := w.Slice(); !sameFingerprints(got, want) {
		t.Errorf("dedup/drop-empty = %v, want %v", got, want)
	}
}

func TestFingerprintWindowDedupKeepsMostRecent(t *testing.T) {
	// "a" appears at both ends; only its most recent (last) occurrence may
	// remain, and the window must still be capped at five entries.
	w := NewFingerprintWindow([]string{"a", "b", "c", "d", "e", "a"})
	want := []string{"b", "c", "d", "e", "a"}
	if got := w.Slice(); !sameFingerprints(got, want) {
		t.Errorf("dedup keeping most recent = %v, want %v", got, want)
	}
}

func TestFingerprintWindowSliceReturnsCopy(t *testing.T) {
	w := NewFingerprintWindow([]string{"a", "b", "c"})
	copyOf := w.Slice()
	copyOf[0] = "MUTATED"
	copyOf = append(copyOf, "EXTRA")

	if got := w.Slice(); !sameFingerprints(got, []string{"a", "b", "c"}) {
		t.Errorf("mutating Slice() result changed the window: %v", got)
	}
	if !w.Contains("a") || w.Contains("MUTATED") || w.Contains("EXTRA") {
		t.Error("window membership changed after mutating a Slice() copy")
	}
}

func TestFingerprintWindowAddValueSemantics(t *testing.T) {
	w := NewFingerprintWindow([]string{"a", "b"})
	w2 := w.Add("c")

	if got := w.Slice(); !sameFingerprints(got, []string{"a", "b"}) {
		t.Errorf("Add modified the receiver: original is now %v", got)
	}
	if w.Contains("c") {
		t.Error("original window unexpectedly contains the added fingerprint")
	}
	if got := w2.Slice(); !sameFingerprints(got, []string{"a", "b", "c"}) {
		t.Errorf("Add result = %v, want [a b c]", got)
	}
	if !w2.Contains("c") {
		t.Error("new window does not contain the added fingerprint")
	}
}

func TestFingerprintWindowAddTrimsAndIgnoresEmpty(t *testing.T) {
	w := NewFingerprintWindow([]string{"a", "b", "c", "d", "e"})

	trimmed := w.Add("f")
	if got := trimmed.Slice(); !sameFingerprints(got, []string{"b", "c", "d", "e", "f"}) {
		t.Errorf("Add to a full window = %v, want trimmed [b c d e f]", got)
	}
	if got := w.Slice(); !sameFingerprints(got, []string{"a", "b", "c", "d", "e"}) {
		t.Errorf("Add modified the full receiver: %v", got)
	}

	ignored := w.Add("")
	if got := ignored.Slice(); !sameFingerprints(got, w.Slice()) {
		t.Errorf("Add of empty fingerprint changed the window to %v", got)
	}
}

func TestFingerprintWindowContains(t *testing.T) {
	w := NewFingerprintWindow([]string{"x", "y"})
	if !w.Contains("x") || !w.Contains("y") {
		t.Error("Contains missed a member of the window")
	}
	if w.Contains("z") || w.Contains("") {
		t.Error("Contains reported a hit for a non-member / empty string")
	}
}

func TestGenerateSkeletonAvoidsFingerprintWindow(t *testing.T) {
	// Build a real window of five distinct generator.Fingerprint values. Using
	// genuine fingerprints matters: arbitrary strings could never collide and
	// would make the avoidance assertion vacuous.
	history := make([]string, 0, FingerprintWindowSize)
	seen := make(map[string]bool)
	for seed := int64(1); len(history) < FingerprintWindowSize && seed < 100; seed++ {
		s, err := GenerateSkeleton(GenerateOptions{Mode: "coop", CompanionCount: 3}, rand.New(rand.NewSource(seed)))
		if err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		if !seen[s.Fingerprint] {
			seen[s.Fingerprint] = true
			history = append(history, s.Fingerprint)
		}
	}
	if len(history) != FingerprintWindowSize {
		t.Fatalf("collected %d distinct fingerprints, want %d", len(history), FingerprintWindowSize)
	}
	window := NewFingerprintWindow(history)
	if len(window.Slice()) != FingerprintWindowSize {
		t.Fatalf("window holds %d entries, want %d", len(window.Slice()), FingerprintWindowSize)
	}

	// Seed 1 would naturally reproduce history[0]. Passing the whole window as
	// the blacklist must force the generator to a fingerprint outside it.
	got, err := GenerateSkeleton(GenerateOptions{
		Mode: "coop", CompanionCount: 3,
		HistoryFingerprints: window.Slice(),
	}, rand.New(rand.NewSource(1)))
	if err != nil {
		t.Fatal(err)
	}
	if window.Contains(got.Fingerprint) {
		t.Errorf("generated fingerprint %q is inside the history window", got.Fingerprint)
	}
	if got.DedupDegraded {
		t.Error("generation unexpectedly degraded while avoiding the window")
	}
}
