// Package consolev2retention owns the single retention matrix used by both the
// production cron and the operator dry-run command.
package consolev2retention

import (
	"fmt"
	"strings"
	"time"
)

const DayMS = int64(24 * time.Hour / time.Millisecond)

type Job struct {
	Name string
	SQL  string
}

// Jobs returns dependency-safe, bounded statements. Every statement accepts
// one PostgreSQL parameter: the maximum number of rows to mutate.
func Jobs() []Job {
	return []Job{
		{"bootstrap_grants", boundedDelete("agent_bootstrap_grants", "expires_at < clock_ms() - 7*day_ms()")},
		{"signature_nonces", boundedDelete("agent_signature_nonces", "expires_at < clock_ms() - 7*day_ms()")},
		{"email_challenges", boundedDelete("v2_email_challenges", "expires_at < clock_ms() - 30*day_ms()")},
		{"handoffs", boundedDelete("console_v2_handoffs", "expires_at < clock_ms() - 7*day_ms()")},
		{"console_sessions", boundedDelete("console_v2_sessions", "absolute_expires_at < clock_ms() - 90*day_ms()")},
		{"credential_sessions", boundedDelete("agent_credential_sessions", "absolute_expires_at < clock_ms() - 90*day_ms()")},
		{"idempotency_responses", boundedDelete("agent_idempotency_requests", "expires_at < clock_ms()")},
		{"telemetry_events", boundedDelete("telemetry_events_v2", "expires_at < clock_ms()")},
		{"usage_sessions", boundedDelete("console_usage_sessions", "updated_at < clock_ms() - 90*day_ms()")},
		{"runtime_leases", boundedDelete("agent_runtime_leases", "lease_until < clock_ms() - day_ms()")},
		{"control_outbox", boundedDelete("control_wakeup_outbox", "status IN ('delivered','dead') AND created_at < clock_ms() - 7*day_ms()")},
		{"feed_exposures", boundedDelete("agent_feed_exposures", "last_seen_at < clock_ms() - 30*day_ms()")},
		{"commands", boundedDelete("agent_commands", "status IN ('completed','failed','expired') AND COALESCE(completed_at, created_at) < clock_ms() - 30*day_ms()")},
		{"attention_expiry", `WITH constants AS (
			SELECT (extract(epoch FROM clock_timestamp())*1000)::bigint AS clock_ms
		), target AS (
			SELECT item.attention_id FROM agent_attention_items item CROSS JOIN constants
			WHERE item.status = 'open' AND item.expires_at IS NOT NULL
			  AND item.expires_at < constants.clock_ms
			ORDER BY item.expires_at, item.attention_id LIMIT $1
		)
		UPDATE agent_attention_items item SET status = 'expired'
		FROM target WHERE item.attention_id = target.attention_id AND item.status = 'open'`},
		{"attention_items", boundedDelete("agent_attention_items", "status IN ('acted','dismissed','expired') AND created_at < clock_ms() - 90*day_ms() AND NOT EXISTS (SELECT 1 FROM agent_commands command WHERE command.attention_id = row.attention_id)")},
		{"activity", boundedDelete("agent_activity_log", "created_at < clock_ms() - 90*day_ms()")},
	}
}

func boundedDelete(table, predicate string) string {
	predicate = strings.ReplaceAll(predicate, "clock_ms()", "constants.clock_ms")
	predicate = strings.ReplaceAll(predicate, "day_ms()", "constants.day_ms")
	return fmt.Sprintf(`WITH constants AS (
		SELECT (extract(epoch FROM clock_timestamp())*1000)::bigint AS clock_ms,
		       %d::bigint AS day_ms
	), target AS (
		SELECT row.ctid FROM %s row CROSS JOIN constants
		WHERE %s LIMIT $1
	)
	DELETE FROM %s row USING target WHERE row.ctid = target.ctid`, DayMS, table, predicate, table)
}
