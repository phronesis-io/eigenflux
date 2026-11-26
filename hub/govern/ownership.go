package govern

import (
	"fmt"
	"sort"
	"sync"
)

// CompanionBinding is the durable answer to "who owns this companion".
//
// The Hub is the authority on this rather than the client for the same reason it
// is the authority on permissions: a client that asserts its own ownership proves
// nothing, and the assertion is exactly what an attacker would forge.
type CompanionBinding struct {
	CompanionID string `json:"companion_id"`
	OwnerID     string `json:"owner_id"`
	BoundAt     int64  `json:"bound_at"`
}

// Ownership is the companion→owner registry (ADR-0009 §2.2).
//
// The map is one-directional on purpose. A reverse index (owner → companions) is
// convenient but would have to stay consistent with the forward one across a
// restore; the forward map is the only fact, and lookups by owner are cheap
// enough to walk.
type Ownership struct {
	mu       sync.Mutex
	bindings map[string]CompanionBinding
}

// NewOwnership returns an empty registry.
func NewOwnership() *Ownership {
	return &Ownership{bindings: make(map[string]CompanionBinding)}
}

// Bind records that ownerID owns companionID.
//
// Idempotent for the same pairing: a client retrying a bind is not an error.
// Re-binding to a different owner is refused rather than transferred, because a
// silent transfer is a hijack primitive — taking over someone's companion would
// be one request away, and that request would carry a valid token.
func (o *Ownership) Bind(companionID, ownerID string, now int64) (CompanionBinding, error) {
	if companionID == "" {
		return CompanionBinding{}, fmt.Errorf("binding requires a companion_id")
	}
	if ownerID == "" {
		return CompanionBinding{}, fmt.Errorf("binding requires an owner_id")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if existing, ok := o.bindings[companionID]; ok {
		if existing.OwnerID != ownerID {
			return CompanionBinding{}, fmt.Errorf(
				"companion %q is already bound to another owner; transfer is not supported",
				companionID)
		}
		return existing, nil
	}
	binding := CompanionBinding{CompanionID: companionID, OwnerID: ownerID, BoundAt: now}
	o.bindings[companionID] = binding
	return binding, nil
}

// OwnerOf reports the owner of a companion and whether it was ever bound.
func (o *Ownership) OwnerOf(companionID string) (string, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	binding, ok := o.bindings[companionID]
	return binding.OwnerID, ok
}

// Get returns the whole binding.
func (o *Ownership) Get(companionID string) (CompanionBinding, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	binding, ok := o.bindings[companionID]
	return binding, ok
}

// Snapshot returns the durable form, sorted by companion id so the state file
// stays byte-stable across runs.
func (o *Ownership) Snapshot() []CompanionBinding {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]CompanionBinding, 0, len(o.bindings))
	for _, b := range o.bindings {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CompanionID < out[j].CompanionID })
	return out
}

// Restore loads bindings, validating each one. Two bindings for the same
// companion with different owners is a corrupt file, not a last-writer-wins
// situation: which owner wins would be arbitrary, and the loser would silently
// lose their companion.
func (o *Ownership) Restore(bindings []CompanionBinding) error {
	seen := make(map[string]string, len(bindings))
	for _, b := range bindings {
		if b.CompanionID == "" {
			return fmt.Errorf("restored binding has no companion_id")
		}
		if b.OwnerID == "" {
			return fmt.Errorf("restored binding for %q has no owner_id", b.CompanionID)
		}
		if owner, dup := seen[b.CompanionID]; dup && owner != b.OwnerID {
			return fmt.Errorf("restored state binds companion %q to two owners (%q and %q)",
				b.CompanionID, owner, b.OwnerID)
		}
		seen[b.CompanionID] = b.OwnerID
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, b := range bindings {
		o.bindings[b.CompanionID] = b
	}
	return nil
}
