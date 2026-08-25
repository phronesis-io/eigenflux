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
		"command_expiry": false, "commands": false, "attention_text_redaction": false, "attention_expiry": false,
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
	}
	for name, found := range required {
		if !found {
			t.Fatalf("missing retention job %q", name)
		}
	}
}
