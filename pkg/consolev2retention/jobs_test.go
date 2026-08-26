package consolev2retention

import (
	"strings"
	"testing"
)

func TestRetentionMatrixIsBounded(t *testing.T) {
	required := map[string]bool{
		"bootstrap_grants": false, "signature_nonces": false, "email_challenges": false,
		"handoffs": false, "console_sessions": false, "credential_sessions": false,
		"idempotency_responses": false, "telemetry_events": false, "usage_sessions": false,
		"runtime_leases": false, "control_outbox": false, "feed_exposures": false,
		"command_expiry": false, "attention_command_expiry_recovery": false,
		"commands": false, "attention_command_payload_redaction": false,
		"control_outbox_orphans":   false,
		"attention_text_redaction": false, "attention_expiry": false,
		"attention_items": false, "activity": false,
	}
	seen := make(map[string]bool)
	for _, job := range Jobs() {
		if seen[job.Name] {
			t.Fatalf("duplicate retention job %q", job.Name)
		}
		seen[job.Name] = true
		if _, tracked := required[job.Name]; tracked {
			required[job.Name] = true
		}
		if !strings.Contains(job.SQL, "$1") {
			t.Fatalf("retention job %q is not batch bounded", job.Name)
		}
		if !strings.Contains(job.SQL, "SKIP LOCKED") {
			t.Fatalf("retention job %q can block or race another worker", job.Name)
		}
		if job.Name == "attention_command_payload_redaction" &&
			(!strings.Contains(job.SQL, "attention_snapshot,title") ||
				!strings.Contains(job.SQL, "attention_snapshot,body") ||
				!strings.Contains(job.SQL, "attention_snapshot,recommendation")) {
			t.Fatalf("Attention command payload redaction does not remove every authored text field")
		}
	}
	for name, found := range required {
		if !found {
			t.Fatalf("missing retention job %q", name)
		}
	}
}

func TestCommandExpiryRespectsLiveClaimLease(t *testing.T) {
	var expiry string
	for _, job := range Jobs() {
		if job.Name == "command_expiry" {
			expiry = job.SQL
			break
		}
	}
	for _, required := range []string{"claim_until <= constants.clock_ms", "FOR UPDATE OF command SKIP LOCKED", "command.status = 'claimed'"} {
		if !strings.Contains(expiry, required) {
			t.Fatalf("command expiry is not lease-safe; missing %q", required)
		}
	}
}
