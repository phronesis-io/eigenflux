package generator

// FingerprintWindowSize is the number of recent matches whose fingerprints
// must not repeat. It mirrors the "within the same companion group's last N
// games (N=5), structural fingerprints must not repeat" rule in
// design/game-brief.md (Layer 2 structural freshness, ~L308) and story-005
// AC6. The window is keyed on generator.Fingerprint, NOT StructuralHash:
// StructuralHash is topology-only and deliberately collides (narrative text is
// excluded), so it cannot express the "no repeated structure" intent. The
// GDD's dedup rule is enforced on generator.Fingerprint in
// invitation.buildProposals (see hub/invitation/manager.go).
const FingerprintWindowSize = 5

// FingerprintWindow holds a bounded, time-ordered list (oldest -> newest) of
// recent match fingerprints. It is a pure value type: every operation returns
// a new window and leaves the receiver untouched, so callers own all
// synchronisation and no concurrency primitives live here. Keep the entries
// field unexported so no caller can mutate the backing slice in place.
type FingerprintWindow struct {
	entries []string
}

// NewFingerprintWindow builds a window from a raw history. Only the last
// FingerprintWindowSize distinct, non-empty fingerprints are kept, in their
// original time order (oldest -> newest). When a fingerprint appears more than
// once, its most recent occurrence is the one retained, because the window
// represents the most recently played games.
func NewFingerprintWindow(history []string) FingerprintWindow {
	seen := make(map[string]bool, FingerprintWindowSize)
	kept := make([]string, 0, FingerprintWindowSize)
	for i := len(history) - 1; i >= 0 && len(kept) < FingerprintWindowSize; i-- {
		fp := history[i]
		if fp == "" || seen[fp] {
			continue
		}
		seen[fp] = true
		kept = append(kept, fp)
	}
	// Reverse the newest-first collection back into oldest -> newest order.
	for i, j := 0, len(kept)-1; i < j; i, j = i+1, j-1 {
		kept[i], kept[j] = kept[j], kept[i]
	}
	return FingerprintWindow{entries: kept}
}

// Contains reports whether fp is currently in the window.
func (w FingerprintWindow) Contains(fp string) bool {
	for _, e := range w.entries {
		if e == fp {
			return true
		}
	}
	return false
}

// Add returns a NEW window with fp appended and the result trimmed to the last
// FingerprintWindowSize entries. It never modifies the receiver (value
// semantics): the backing slice is copied before appending, so the caller may
// safely retain and reuse the original window. An empty fp is ignored.
func (w FingerprintWindow) Add(fp string) FingerprintWindow {
	entries := make([]string, 0, FingerprintWindowSize)
	entries = append(entries, w.entries...)
	if fp != "" {
		entries = append(entries, fp)
	}
	if len(entries) > FingerprintWindowSize {
		entries = entries[len(entries)-FingerprintWindowSize:]
	}
	return FingerprintWindow{entries: entries}
}

// Slice returns a copy of the window's fingerprints in oldest -> newest order.
// Callers cannot mutate the window's internal state through the returned slice.
func (w FingerprintWindow) Slice() []string {
	out := make([]string, len(w.entries))
	copy(out, w.entries)
	return out
}
