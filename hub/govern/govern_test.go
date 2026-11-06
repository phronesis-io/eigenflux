package govern

import (
	"strings"
	"testing"
	"time"
)

func nov(t *testing.T) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, "2026-11-07T00:30:00Z")
	if err != nil {
		t.Fatalf("parse time: %v", err)
	}
	return ts
}

func TestBudgetAllowsWithinCapAndRefusesWithDowngrade(t *testing.T) {
	ledger := NewBudgetLedger()
	now := nov(t)

	if err := ledger.SetLimit("comp_a", 1000); err != nil {
		t.Fatalf("SetLimit: %v", err)
	}
	first := ledger.Record("comp_a", 600, now)
	if !first.Allowed || first.Downgrade != "" {
		t.Fatalf("600 of 1000 should be allowed: %+v", first)
	}

	second := ledger.Record("comp_a", 500, now)
	if second.Allowed {
		t.Fatalf("500 more should exceed the 1000 cap: %+v", second)
	}
	if second.Downgrade != DowngradeRuleEngine {
		t.Fatalf("downgrade = %q, want %q", second.Downgrade, DowngradeRuleEngine)
	}
	if second.SpentBefore != 600 || second.Limit != 1000 || second.Requested != 500 {
		t.Fatalf("refusal must carry the numbers: %+v", second)
	}
	if !strings.Contains(second.Reason, "600") || !strings.Contains(second.Reason, "1000") {
		t.Fatalf("reason does not explain the arithmetic: %q", second.Reason)
	}

	// A refused request must not consume budget it never used.
	limit, spent, configured := ledger.Usage("comp_a", now)
	if !configured || limit != 1000 || spent != 600 {
		t.Fatalf("usage after refusal = %d/%d configured=%v", spent, limit, configured)
	}
}

func TestBudgetUsesDefaultCapAndRollsOverMonthly(t *testing.T) {
	ledger := NewBudgetLedger()
	now := nov(t)

	limit, spent, configured := ledger.Usage("comp_unset", now)
	if configured {
		t.Fatal("a companion without a configured limit must report as unconfigured")
	}
	if limit != DefaultMonthlyTokens || spent != 0 {
		t.Fatalf("default usage = %d/%d", spent, limit)
	}

	decision := ledger.Record("comp_unset", DefaultMonthlyTokens+1, now)
	if decision.Allowed {
		t.Fatal("a request above the default cap must be refused")
	}
	if !strings.Contains(decision.Reason, "ADR-0004") {
		t.Fatalf("a default-cap refusal must say the cap is an owner decision, not a GDD number: %q", decision.Reason)
	}

	// Next month starts from zero: the cap is monthly, not lifetime.
	if err := ledger.SetLimit("comp_unset", 100); err != nil {
		t.Fatalf("SetLimit: %v", err)
	}
	ledger.Record("comp_unset", 100, now)
	next, err := time.Parse(time.RFC3339, "2026-12-01T00:00:00Z")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, spent, _ := ledger.Usage("comp_unset", next); spent != 0 {
		t.Fatalf("December spend = %d, want 0", spent)
	}
	if got := ledger.Record("comp_unset", 100, next); !got.Allowed {
		t.Fatalf("a new month should allow the same spend again: %+v", got)
	}
}

func TestBudgetRejectsNonPositiveLimitAndNegativeRequest(t *testing.T) {
	ledger := NewBudgetLedger()
	if err := ledger.SetLimit("comp_a", 0); err == nil {
		t.Fatal("a zero limit must be rejected, otherwise it means unlimited")
	}
	if err := ledger.SetLimit("", 100); err == nil {
		t.Fatal("a limit without a companion must be rejected")
	}
	if got := ledger.Decide("comp_a", -5, nov(t)); got.Allowed {
		t.Fatalf("negative request allowed: %+v", got)
	}
}

func TestPermissionDeniesByDefaultAndByTier(t *testing.T) {
	registry := NewRegistry()
	now := nov(t).UnixMilli()

	unconfigured := registry.Authorize("comp_a", "read_files", TierReadOnly, now)
	if unconfigured.Allowed || unconfigured.Code != DenyNoConfig {
		t.Fatalf("unconfigured companion = %+v", unconfigured)
	}

	if _, err := registry.Set("comp_a", "superuser", now); err == nil {
		t.Fatal("unknown tier must be rejected locally")
	}
	if _, err := registry.Set("comp_a", TierReadOnly, now); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got := registry.Authorize("comp_a", "read_files", TierReadOnly, now); !got.Allowed {
		t.Fatalf("read-only tier should allow reads: %+v", got)
	}
	denied := registry.Authorize("comp_a", "send_message", TierRestrictedWrite, now)
	if denied.Allowed || denied.Code != DenyTierTooLow {
		t.Fatalf("tier escalation = %+v", denied)
	}
	if !strings.Contains(denied.Reason, TierReadOnly) {
		t.Fatalf("denial should state the configured tier: %q", denied.Reason)
	}
}

func TestPermissionIsolationBetweenCompanions(t *testing.T) {
	registry := NewRegistry()
	now := nov(t).UnixMilli()
	if _, err := registry.Set("comp_a", TierHighImpact, now); err != nil {
		t.Fatalf("Set: %v", err)
	}
	// comp_b has no configuration at all; comp_a's grant must not leak.
	if got := registry.Authorize("comp_b", "delete_files", TierReadOnly, now); got.Allowed || got.Code != DenyNoConfig {
		t.Fatalf("comp_b inherited comp_a's grant: %+v", got)
	}
	if _, err := registry.Set("comp_b", TierReadOnly, now); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got := registry.Authorize("comp_b", "delete_files", TierHighImpact, now); got.Allowed || got.Code != DenyTierTooLow {
		t.Fatalf("comp_b reached high impact: %+v", got)
	}
}

func TestHighImpactNeedsSingleUseApproval(t *testing.T) {
	registry := NewRegistry()
	now := nov(t).UnixMilli()
	if _, err := registry.Set("comp_a", TierHighImpact, now); err != nil {
		t.Fatalf("Set: %v", err)
	}

	noApproval := registry.Authorize("comp_a", "delete_files", TierHighImpact, now)
	if noApproval.Allowed || noApproval.Code != DenyApprovalNeeded {
		t.Fatalf("high impact without approval = %+v", noApproval)
	}

	approval, err := registry.Approve("comp_a", "delete_files", 60, now)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	// An approval for one operation must not authorise another.
	other := registry.Authorize("comp_a", "execute_command", TierHighImpact, now)
	if other.Allowed {
		t.Fatalf("approval for delete_files authorised execute_command: %+v", other)
	}
	first := registry.Authorize("comp_a", "delete_files", TierHighImpact, now)
	if !first.Allowed || !strings.Contains(first.Reason, approval.ID) {
		t.Fatalf("approved call = %+v", first)
	}
	// Single use: the second identical call needs a new approval.
	second := registry.Authorize("comp_a", "delete_files", TierHighImpact, now)
	if second.Allowed || second.Code != DenyApprovalNeeded {
		t.Fatalf("approval was reused: %+v", second)
	}
}

func TestExpiredApprovalIsRefused(t *testing.T) {
	registry := NewRegistry()
	now := nov(t).UnixMilli()
	if _, err := registry.Set("comp_a", TierHighImpact, now); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if _, err := registry.Approve("comp_a", "delete_files", 1, now); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	later := now + 5000
	if got := registry.Authorize("comp_a", "delete_files", TierHighImpact, later); got.Allowed {
		t.Fatalf("expired approval was accepted: %+v", got)
	}
}

func TestRevokedPermissionIsDenied(t *testing.T) {
	registry := NewRegistry()
	now := nov(t).UnixMilli()
	if _, err := registry.Set("comp_a", TierRestrictedWrite, now); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if _, err := registry.Revoke("comp_a", now+1); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	got := registry.Authorize("comp_a", "send_message", TierRestrictedWrite, now+2)
	if got.Allowed || got.Code != DenyRevoked {
		t.Fatalf("revoked permission = %+v", got)
	}
	if got.Configured != TierRestrictedWrite {
		t.Fatalf("revocation erased the tier from the record: %+v", got)
	}
}

func TestAuditChainDetectsTampering(t *testing.T) {
	log := NewAuditLog()
	now := nov(t).UnixMilli()
	for i, action := range []struct{ companion, op, outcome string }{
		{"comp_a", "read_files", OutcomeAllowed},
		{"comp_a", "delete_files", OutcomeDenied},
		{"comp_b", "send_message", OutcomeAllowed},
	} {
		entry, err := log.Append(AuditEntry{
			CompanionID: action.companion, Operation: action.op,
			Tier: TierReadOnly, Outcome: action.outcome, Detail: "test", At: now + int64(i),
		})
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
		if entry.Seq != int64(i+1) || !strings.HasPrefix(entry.Hash, "sha256:") {
			t.Fatalf("entry = %+v", entry)
		}
	}
	if err := log.Verify(); err != nil {
		t.Fatalf("Verify on a fresh log: %v", err)
	}
	if got := log.Entries("comp_a"); len(got) != 2 {
		t.Fatalf("comp_a entries = %d", len(got))
	}
	if got := log.Entries(""); len(got) != 3 {
		t.Fatalf("all entries = %d", len(got))
	}

	t.Run("edited outcome", func(t *testing.T) {
		broken := NewAuditLog()
		built := log.Entries("")
		built[1].Outcome = OutcomeAllowed
		broken.Tamper(func([]AuditEntry) []AuditEntry { return built })
		err := broken.Verify()
		if err == nil || !strings.Contains(err.Error(), "hash mismatch") {
			t.Fatalf("Verify = %v", err)
		}
	})

	t.Run("removed entry", func(t *testing.T) {
		broken := NewAuditLog()
		built := log.Entries("")
		trimmed := []AuditEntry{built[0], built[2]}
		broken.Tamper(func([]AuditEntry) []AuditEntry { return trimmed })
		err := broken.Verify()
		if err == nil || !strings.Contains(err.Error(), "reordered or removed") {
			t.Fatalf("Verify = %v", err)
		}
	})

	t.Run("reordered entries", func(t *testing.T) {
		broken := NewAuditLog()
		built := log.Entries("")
		swapped := []AuditEntry{built[1], built[0], built[2]}
		broken.Tamper(func([]AuditEntry) []AuditEntry { return swapped })
		err := broken.Verify()
		if err == nil {
			t.Fatal("reordered log verified")
		}
	})
}

func TestAuditRejectsIncompleteEntries(t *testing.T) {
	log := NewAuditLog()
	if _, err := log.Append(AuditEntry{Operation: "read", Outcome: OutcomeAllowed}); err == nil {
		t.Fatal("an entry without a companion must be rejected")
	}
	if _, err := log.Append(AuditEntry{CompanionID: "comp_a", Operation: "read"}); err == nil {
		t.Fatal("an entry without an outcome must be rejected")
	}
}

func TestModelConsistency(t *testing.T) {
	same := CheckModelVersions([]ModelUsage{
		{SeatID: "seat_0", ModelVersion: "claude-sonnet-4-20250514"},
		{SeatID: "seat_1", ModelVersion: "claude-sonnet-4-20250514"},
		{SeatID: "seat_2", ModelVersion: "official-v1", Official: true},
	})
	if !same.Consistent || same.Code != ModelConsistent {
		t.Fatalf("same versions = %+v", same)
	}
	if len(same.Versions) != 1 {
		t.Fatalf("official seat should not add a version: %v", same.Versions)
	}

	mixed := CheckModelVersions([]ModelUsage{
		{SeatID: "seat_0", ModelVersion: "claude-sonnet-4-20250514"},
		{SeatID: "seat_1", ModelVersion: "claude-opus-4-20250514"},
	})
	if mixed.Consistent || mixed.Code != ModelMixedVersions {
		t.Fatalf("mixed versions = %+v", mixed)
	}
	if len(mixed.Versions) != 2 || !strings.Contains(mixed.Reason, "2 different") {
		t.Fatalf("mixed detail = %+v", mixed)
	}

	absent := CheckModelVersions([]ModelUsage{
		{SeatID: "seat_0", ModelVersion: "claude-sonnet-4-20250514"},
		{SeatID: "seat_1"},
	})
	if absent.Consistent || absent.Code != ModelVersionAbsent {
		t.Fatalf("missing version must not pass: %+v", absent)
	}
	if !strings.Contains(absent.Reason, "seat_1") {
		t.Fatalf("reason should name the seat: %q", absent.Reason)
	}

	officialOnly := CheckModelVersions([]ModelUsage{{SeatID: "seat_2", Official: true}})
	if !officialOnly.Consistent || officialOnly.Code != ModelNoParticipants {
		t.Fatalf("official-only match = %+v", officialOnly)
	}
}

func TestRepriseModelChangeMustBeDeclared(t *testing.T) {
	original := CheckModelVersions([]ModelUsage{{SeatID: "seat_0", ModelVersion: "claude-sonnet-4-20250514"}})
	replayed := CheckModelVersions([]ModelUsage{{SeatID: "seat_0", ModelVersion: "claude-sonnet-4-20250514"}})
	if err := CheckRepriseModelChange(original, replayed, false); err != nil {
		t.Fatalf("identical versions should pass: %v", err)
	}

	swapped := CheckModelVersions([]ModelUsage{{SeatID: "seat_0", ModelVersion: "claude-opus-4-20250514"}})
	if err := CheckRepriseModelChange(original, swapped, false); err == nil {
		t.Fatal("an undeclared model swap must be rejected")
	}
	if err := CheckRepriseModelChange(original, swapped, true); err != nil {
		t.Fatalf("a declared different_model reprise should pass: %v", err)
	}
}
