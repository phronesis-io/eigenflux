// Package govern holds the phase-1 controls S5 asks for: a per-companion token
// budget with an explicit downgrade path, three-tier tool permissions with
// per-execution approval for high-impact actions, a tamper-evident audit log,
// and a model-version consistency check.
//
// Why this lives on the Hub rather than in the client: every one of these is an
// authority decision. A client that both grants permission and records the
// grant proves nothing. The Hub holds the state, the client asks, and the audit
// log is append-only and hash-chained so a removed or edited entry is
// detectable.
package govern

import (
	"fmt"
	"sync"
	"time"
)

// DefaultMonthlyTokens is the ratified per-companion monthly cap
// (ADR-0004 option A, contracts §8). The GDD is silent on this number, so it is
// an owner decision and is named as such wherever it is reported.
const DefaultMonthlyTokens = 200_000

// DowngradeRuleEngine is the only downgrade the budget path may take. The GDD
// forbids a silent quality reduction, so an over-budget request is refused with
// this explicit alternative rather than being served at lower quality.
const DowngradeRuleEngine = "rule_engine"

// BudgetDecision is the answer to "may this companion spend N tokens now?".
// It always carries the numbers it was decided from: a refusal that does not
// say how far over budget the caller is cannot be acted on.
type BudgetDecision struct {
	CompanionID string `json:"companion_id"`
	Month       string `json:"month"`
	Limit       int    `json:"limit"`
	SpentBefore int    `json:"spent_before"`
	Requested   int    `json:"requested"`
	Allowed     bool   `json:"allowed"`
	Downgrade   string `json:"downgrade,omitempty"`
	Reason      string `json:"reason"`
}

// BudgetLedger tracks monthly token spend per companion.
type BudgetLedger struct {
	mu     sync.Mutex
	limits map[string]int // companionID -> monthly limit
	spent  map[string]int // companionID|month -> tokens used
}

// NewBudgetLedger returns a ledger with no per-companion limits set.
func NewBudgetLedger() *BudgetLedger {
	return &BudgetLedger{limits: make(map[string]int), spent: make(map[string]int)}
}

// MonthKey is the UTC calendar month a spend is accounted against. It is a
// function of the clock, never of the caller, so a client cannot report usage
// into a month it prefers.
func MonthKey(now time.Time) string { return now.UTC().Format("2006-01") }

func spendKey(companionID, month string) string { return companionID + "|" + month }

// SetLimit sets a companion's monthly token allowance. A non-positive limit is
// rejected: "zero" and "unlimited" must not be the same value, because the
// product has no unlimited mode.
func (b *BudgetLedger) SetLimit(companionID string, monthly int) error {
	if companionID == "" {
		return fmt.Errorf("budget limit requires a companion_id")
	}
	if monthly <= 0 {
		return fmt.Errorf("monthly token limit must be positive, got %d", monthly)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.limits[companionID] = monthly
	return nil
}

// Limit returns the effective monthly limit and whether it was configured.
func (b *BudgetLedger) Limit(companionID string) (int, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	limit, ok := b.limits[companionID]
	return limit, ok
}

// Usage reports the current month's spend and effective limit.
func (b *BudgetLedger) Usage(companionID string, now time.Time) (limit, spent int, configured bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	limit, configured = b.limits[companionID]
	if !configured {
		limit = DefaultMonthlyTokens
	}
	return limit, b.spent[spendKey(companionID, MonthKey(now))], configured
}

// Decide answers whether a request fits the remaining budget without recording
// anything. Callers use it as a pre-flight check.
func (b *BudgetLedger) Decide(companionID string, requested int, now time.Time) BudgetDecision {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.decideLocked(companionID, requested, now)
}

func (b *BudgetLedger) decideLocked(companionID string, requested int, now time.Time) BudgetDecision {
	month := MonthKey(now)
	limit, configured := b.limits[companionID]
	if !configured {
		limit = DefaultMonthlyTokens
	}
	spent := b.spent[spendKey(companionID, month)]
	decision := BudgetDecision{
		CompanionID: companionID, Month: month, Limit: limit,
		SpentBefore: spent, Requested: requested,
	}
	switch {
	case requested < 0:
		decision.Reason = fmt.Sprintf("requested token count must not be negative, got %d", requested)
	case spent+requested <= limit:
		decision.Allowed = true
		// The number is post-request: Record books the spend before returning,
		// so "all 1000 remain" for a 300-token call would be a lie in the audit
		// log. Decide does not book, and the same wording still reads correctly
		// as a projection.
		decision.Reason = fmt.Sprintf("%d of %d tokens remain this month after this %d-token request",
			limit-spent-requested, limit, requested)
	default:
		decision.Downgrade = DowngradeRuleEngine
		decision.Reason = fmt.Sprintf("request of %d tokens exceeds the %d-token monthly cap (%d already spent); downgrade to the local rule engine",
			requested, limit, spent)
		if !configured {
			decision.Reason += " using the default cap from ADR-0004"
		}
	}
	return decision
}

// Record decides and, when allowed, books the spend. A refused request books
// nothing: a downgraded call must not consume budget it never used.
func (b *BudgetLedger) Record(companionID string, used int, now time.Time) BudgetDecision {
	b.mu.Lock()
	defer b.mu.Unlock()
	decision := b.decideLocked(companionID, used, now)
	if decision.Allowed {
		b.spent[spendKey(companionID, decision.Month)] += used
	}
	return decision
}

// Snapshot returns the durable part of the ledger. Spend is keyed by
// "companion|month" exactly as the ledger keys it, so a restore cannot shift a
// month's spend into a different month.
func (b *BudgetLedger) Snapshot() BudgetSnapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	snap := BudgetSnapshot{
		Limits: make(map[string]int, len(b.limits)),
		Spent:  make(map[string]int, len(b.spent)),
	}
	for k, v := range b.limits {
		snap.Limits[k] = v
	}
	for k, v := range b.spent {
		snap.Spent[k] = v
	}
	return snap
}

// Restore loads a snapshot, validating every entry. A restored limit must be
// positive and a restored spend must not be negative: a corrupted file must fail
// loudly instead of turning into a negative balance that spends forever.
func (b *BudgetLedger) Restore(snap BudgetSnapshot) error {
	for companionID, limit := range snap.Limits {
		if companionID == "" {
			return fmt.Errorf("restored budget limit has no companion_id")
		}
		if limit <= 0 {
			return fmt.Errorf("restored limit for %q must be positive, got %d", companionID, limit)
		}
	}
	for key, spent := range snap.Spent {
		if key == "" {
			return fmt.Errorf("restored spend entry has no key")
		}
		if spent < 0 {
			return fmt.Errorf("restored spend for %q must not be negative, got %d", key, spent)
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for companionID, limit := range snap.Limits {
		b.limits[companionID] = limit
	}
	for key, spent := range snap.Spent {
		b.spent[key] = spent
	}
	return nil
}
