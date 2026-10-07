package consolev2retention

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestRetentionMatrixIsBounded(t *testing.T) {
	required := map[string]bool{
		"bootstrap_grants": false, "signature_nonces": false, "email_challenges": false,
		"handoffs": false, "console_sessions": false, "credential_sessions": false,
		"idempotency_responses": false, "telemetry_events": false, "usage_sessions": false,
		"runtime_leases": false, "control_outbox": false, "feed_exposures": false,
		"commission_notifications_pending": false, "commission_notifications_acknowledged": false,
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

// TestRetentionDeletesGuardNonCascadingReferences fails when a migration adds a
// foreign key without ON DELETE to a table that a retention job deletes from,
// unless the job excludes rows still referenced through that column. Such a
// reference makes the whole bounded DELETE fail and stalls retention.
func TestRetentionDeletesGuardNonCascadingReferences(t *testing.T) {
	deleteTable := regexp.MustCompile(`DELETE FROM (\w+) row`)
	deletes := map[string][]string{}
	for _, job := range Jobs() {
		if match := deleteTable.FindStringSubmatch(job.SQL); match != nil {
			deletes[match[1]] = append(deletes[match[1]], job.Name)
		}
	}
	jobSQL := map[string]string{}
	for _, job := range Jobs() {
		jobSQL[job.Name] = job.SQL
	}

	paths, err := filepath.Glob("../../migrations/*.sql")
	if err != nil || len(paths) == 0 {
		t.Fatalf("migrations not found: %v", err)
	}
	tableStmt := regexp.MustCompile(`(?i)^\s*(?:CREATE TABLE(?: IF NOT EXISTS)?|ALTER TABLE(?: IF EXISTS)?)\s+(\w+)`)
	inlineRef := regexp.MustCompile(`(?i)^\s*(?:ADD COLUMN(?: IF NOT EXISTS)?\s+)?(\w+)\s+[^,]*?\bREFERENCES\s+(\w+)\s*\(`)
	foreignKey := regexp.MustCompile(`(?i)FOREIGN KEY\s*\(([^)]*)\)`)
	tableRef := regexp.MustCompile(`(?i)\bREFERENCES\s+(\w+)\s*\(`)
	checked := 0
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		up, _, _ := strings.Cut(string(raw), "-- +goose Down")
		lines := strings.Split(up, "\n")
		current := ""
		for i, line := range lines {
			if match := tableStmt.FindStringSubmatch(line); match != nil {
				current = strings.ToLower(match[1])
			}
			ref := tableRef.FindStringSubmatch(line)
			if ref == nil {
				continue
			}
			target := strings.ToLower(ref[1])
			jobs := deletes[target]
			if len(jobs) == 0 || strings.Contains(strings.ToUpper(line), "ON DELETE") {
				continue
			}
			var columns []string
			if match := foreignKey.FindStringSubmatch(line); match != nil {
				columns = strings.Split(match[1], ",")
			} else if match := inlineRef.FindStringSubmatch(line); match != nil && !strings.EqualFold(match[1], "FOREIGN") {
				columns = []string{match[1]}
			} else if i > 0 {
				if match := foreignKey.FindStringSubmatch(lines[i-1]); match != nil {
					columns = strings.Split(match[1], ",")
				}
			}
			if current == "" || len(columns) == 0 {
				t.Fatalf("%s:%d: cannot resolve the referencing table or column for %q", filepath.Base(path), i+1, strings.TrimSpace(line))
			}
			for _, name := range jobs {
				// A composite reference is excluded when any of its columns is
				// correlated to the deleted row (the remaining columns only
				// narrow an already unique match).
				sql := jobSQL[name]
				guarded := false
				for _, column := range columns {
					column = strings.ToLower(strings.TrimSpace(column))
					guarded = guarded || strings.Contains(sql, "."+column+" = row.")
				}
				if !strings.Contains(sql, current) || !guarded {
					t.Errorf("%s:%d: %s(%s) references %s without ON DELETE, but retention job %q does not exclude referenced rows",
						filepath.Base(path), i+1, current, strings.Join(columns, ","), target, name)
				}
				checked++
			}
		}
	}
	if checked == 0 {
		t.Fatal("no non-cascading references to retention tables were found; the migration scan is broken")
	}
}
