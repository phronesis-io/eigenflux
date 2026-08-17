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
		{"feed_payload_redaction", `WITH constants AS (
			SELECT (extract(epoch FROM clock_timestamp())*1000)::bigint AS clock_ms, 86400000::bigint AS day_ms
		), target AS (
			SELECT item.ctid FROM feed_batch_items item JOIN feed_batches batch ON batch.batch_id = item.batch_id
			CROSS JOIN constants
			WHERE batch.status IN ('acked','dead','expired') AND batch.created_at < constants.clock_ms - 7*constants.day_ms
			  AND COALESCE(item.payload_snapshot->>'redacted', 'false') <> 'true'
			ORDER BY batch.created_at, item.batch_item_id LIMIT $1
		)
		UPDATE feed_batch_items item SET payload_snapshot = jsonb_build_object(
			'source_ref', item.payload_snapshot->'source_ref', 'redacted', true),
			intent_match_snapshot = CASE WHEN item.intent_match_snapshot IS NULL THEN NULL ELSE
				jsonb_build_object('status', item.intent_match_snapshot->'status',
					'matched_intent_ids', COALESCE(item.intent_match_snapshot->'matched_intent_ids', '[]'::jsonb)) END,
			last_error = NULL, updated_at = constants.clock_ms
		FROM target CROSS JOIN constants WHERE item.ctid = target.ctid`},
		{"terminal_consumer_state", `WITH constants AS (
			SELECT (extract(epoch FROM clock_timestamp())*1000)::bigint AS clock_ms, 86400000::bigint AS day_ms
		), target AS (
			SELECT state.ctid FROM feed_consumer_state state JOIN feed_batches batch ON batch.batch_id = state.active_batch_id
			CROSS JOIN constants
			WHERE batch.status IN ('acked','dead','expired') AND batch.created_at < constants.clock_ms - 30*constants.day_ms
			ORDER BY batch.created_at, batch.batch_id LIMIT $1
		)
		UPDATE feed_consumer_state state SET active_batch_id = NULL, updated_at = constants.clock_ms
		FROM target CROSS JOIN constants WHERE state.ctid = target.ctid`},
		{"feed_batches", boundedDelete("feed_batches", "status IN ('acked','dead','expired') AND created_at < clock_ms() - 30*day_ms() AND NOT EXISTS (SELECT 1 FROM feed_consumer_state state WHERE state.active_batch_id = row.batch_id)")},
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
