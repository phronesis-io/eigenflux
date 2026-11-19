package govern

import (
	"fmt"
	"sort"
	"sync"
)

// The three permission tiers from the GDD "Tool permissions" table. Order
// matters: a companion may only do what its configured tier reaches.
const (
	TierReadOnly        = "read_only"
	TierRestrictedWrite = "restricted_write"
	TierHighImpact      = "high_impact"
)

// tierRank makes the tier comparison explicit instead of relying on string
// ordering, which would silently accept an unknown tier.
var tierRank = map[string]int{
	TierReadOnly:        1,
	TierRestrictedWrite: 2,
	TierHighImpact:      3,
}

// PermissionConfig is one companion's independent tool configuration. The GDD
// requires each companion to be configured separately, so there is no inherited
// or shared setting.
type PermissionConfig struct {
	CompanionID string `json:"companion_id"`
	Tier        string `json:"tier"`
	Revoked     bool   `json:"revoked"`
	UpdatedAt   int64  `json:"updated_at"`
}

// Approval authorises exactly one high-impact operation, once. It is single-use
// and expiring by construction: a standing approval would defeat the GDD rule
// that high-impact actions need confirmation at the moment of execution.
type Approval struct {
	ID          string `json:"id"`
	CompanionID string `json:"companion_id"`
	Operation   string `json:"operation"`
	ExpiresAt   int64  `json:"expires_at"`
	Used        bool   `json:"used"`
}

// PermissionResult is the answer to an authorisation request.
type PermissionResult struct {
	Allowed     bool   `json:"allowed"`
	CompanionID string `json:"companion_id"`
	Operation   string `json:"operation"`
	Required    string `json:"required_tier"`
	Configured  string `json:"configured_tier,omitempty"`
	Code        string `json:"code"`
	Reason      string `json:"reason"`
}

// Denial codes. They are stable so a client can react without parsing prose.
const (
	DenyNoConfig       = "no_permission_configured"
	DenyRevoked        = "permissions_revoked"
	DenyTierTooLow     = "tier_too_low"
	DenyApprovalNeeded = "approval_required"
	DenyUnknownTier    = "unknown_tier"
)

// Registry holds per-companion permissions and the outstanding approvals.
type Registry struct {
	mu        sync.Mutex
	configs   map[string]PermissionConfig
	approvals map[string][]*Approval
	seq       int64
}

// NewRegistry returns an empty registry. An unconfigured companion is denied
// rather than defaulted: no entry means no grant.
func NewRegistry() *Registry {
	return &Registry{configs: make(map[string]PermissionConfig), approvals: make(map[string][]*Approval)}
}

// Set configures one companion's tier. Unknown tiers are rejected locally so a
// typo cannot silently grant the highest level.
func (r *Registry) Set(companionID, tier string, now int64) (PermissionConfig, error) {
	if companionID == "" {
		return PermissionConfig{}, fmt.Errorf("permissions require a companion_id")
	}
	if _, ok := tierRank[tier]; !ok {
		return PermissionConfig{}, fmt.Errorf("unknown permission tier %q", tier)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cfg := PermissionConfig{CompanionID: companionID, Tier: tier, UpdatedAt: now}
	r.configs[companionID] = cfg
	return cfg, nil
}

// Get returns a companion's configuration.
func (r *Registry) Get(companionID string) (PermissionConfig, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cfg, ok := r.configs[companionID]
	return cfg, ok
}

// Revoke withdraws a companion's permission without erasing the tier, so an
// audit trail can still show what was granted before the revocation.
func (r *Registry) Revoke(companionID string, now int64) (PermissionConfig, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cfg, ok := r.configs[companionID]
	if !ok {
		return PermissionConfig{}, fmt.Errorf("no permission configuration for %q", companionID)
	}
	cfg.Revoked = true
	cfg.UpdatedAt = now
	r.configs[companionID] = cfg
	return cfg, nil
}

// Approve issues a single-use approval for one high-impact operation.
func (r *Registry) Approve(companionID, operation string, ttlSeconds int, now int64) (Approval, error) {
	if companionID == "" || operation == "" {
		return Approval{}, fmt.Errorf("approval requires companion_id and operation")
	}
	if ttlSeconds <= 0 {
		return Approval{}, fmt.Errorf("approval ttl must be positive, got %d", ttlSeconds)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	a := &Approval{
		ID:          fmt.Sprintf("appr_%s_%d", companionID, r.seq),
		CompanionID: companionID, Operation: operation,
		ExpiresAt: now + int64(ttlSeconds)*1000,
	}
	r.approvals[companionID] = append(r.approvals[companionID], a)
	return *a, nil
}

// Authorize decides whether a companion may perform an operation requiring the
// given tier, consuming an approval when one is needed.
//
// Isolation is structural: the lookup is by companion id only, so a grant for
// one companion can never satisfy another's request.
func (r *Registry) Authorize(companionID, operation, requiredTier string, now int64) PermissionResult {
	result := PermissionResult{CompanionID: companionID, Operation: operation, Required: requiredTier}
	required, ok := tierRank[requiredTier]
	if !ok {
		result.Code = DenyUnknownTier
		result.Reason = fmt.Sprintf("unknown required tier %q", requiredTier)
		return result
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	cfg, ok := r.configs[companionID]
	if !ok {
		result.Code = DenyNoConfig
		result.Reason = fmt.Sprintf("companion %q has no permission configuration", companionID)
		return result
	}
	result.Configured = cfg.Tier
	if cfg.Revoked {
		result.Code = DenyRevoked
		result.Reason = fmt.Sprintf("companion %q permissions were revoked", companionID)
		return result
	}
	if tierRank[cfg.Tier] < required {
		result.Code = DenyTierTooLow
		result.Reason = fmt.Sprintf("companion %q is at %s but %q requires %s",
			companionID, cfg.Tier, operation, requiredTier)
		return result
	}

	if required == tierRank[TierHighImpact] {
		approval := r.consumeApprovalLocked(companionID, operation, now)
		if approval == nil {
			result.Code = DenyApprovalNeeded
			result.Reason = fmt.Sprintf("%q is high impact and needs a fresh single-use approval", operation)
			return result
		}
		result.Allowed = true
		result.Code = "approved"
		result.Reason = fmt.Sprintf("high-impact operation approved once by %s", approval.ID)
		return result
	}

	result.Allowed = true
	result.Code = "allowed"
	result.Reason = fmt.Sprintf("companion %q is at %s, which covers %s", companionID, cfg.Tier, requiredTier)
	return result
}

// consumeApprovalLocked finds an unused, unexpired approval for the operation
// and burns it. A mismatched operation does not consume anything.
func (r *Registry) consumeApprovalLocked(companionID, operation string, now int64) *Approval {
	for _, a := range r.approvals[companionID] {
		if a.Used || a.Operation != operation {
			continue
		}
		if now > a.ExpiresAt {
			continue
		}
		a.Used = true
		return a
	}
	return nil
}

// Approvals returns a companion's approvals, for diagnostics.
func (r *Registry) Approvals(companionID string) []Approval {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Approval, 0, len(r.approvals[companionID]))
	for _, a := range r.approvals[companionID] {
		out = append(out, *a)
	}
	return out
}

// Snapshot returns the durable part of the registry, sorted by companion id so
// the state file is byte-stable across runs.
//
// Outstanding approvals are deliberately excluded: an approval authorises one
// high-impact action at the moment of execution, and carrying it across a
// restart would let a grant issued before the process died authorise an action
// after it came back.
func (r *Registry) Snapshot() []PermissionConfig {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]PermissionConfig, 0, len(r.configs))
	for _, cfg := range r.configs {
		out = append(out, cfg)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CompanionID < out[j].CompanionID })
	return out
}

// Restore loads permissions from a snapshot. It rejects what Set would reject
// rather than trusting the file: a state file is an input like any other, and an
// unknown tier read back from disk must not become a working grant.
func (r *Registry) Restore(configs []PermissionConfig) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, cfg := range configs {
		if cfg.CompanionID == "" {
			return fmt.Errorf("restored permission entry has no companion_id")
		}
		if _, ok := tierRank[cfg.Tier]; !ok {
			return fmt.Errorf("restored permission for %q has unknown tier %q", cfg.CompanionID, cfg.Tier)
		}
	}
	for _, cfg := range configs {
		r.configs[cfg.CompanionID] = cfg
	}
	return nil
}
