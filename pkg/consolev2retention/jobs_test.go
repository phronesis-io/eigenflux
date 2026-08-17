package consolev2retention

import (
	"strings"
	"testing"
)

func TestRetentionMatrixIsBoundedAndPreservesFeedSourceReference(t *testing.T) {
	required := map[string]bool{
		"bootstrap_grants": false, "signature_nonces": false, "email_challenges": false,
		"handoffs": false, "console_sessions": false, "credential_sessions": false,
		"idempotency_responses": false, "telemetry_events": false, "usage_sessions": false,
		"runtime_leases": false, "control_outbox": false, "feed_payload_redaction": false,
		"terminal_consumer_state": false, "feed_batches": false, "attention_expiry": false,
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
		if job.Name == "feed_payload_redaction" {
			if !strings.Contains(job.SQL, "'source_ref'") || strings.Contains(job.SQL, "DELETE FROM feed_batch_items") {
				t.Fatal("Feed redaction must preserve source_ref and must not hard-delete items before parent retention")
			}
		}
	}
	for name, found := range required {
		if !found {
			t.Fatalf("missing retention job %q", name)
		}
	}
}
