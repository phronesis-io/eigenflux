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
		{"command_expiry", `WITH constants AS (
			SELECT (extract(epoch FROM clock_timestamp())*1000)::bigint AS clock_ms
		), target AS (
			SELECT command.command_id FROM agent_commands command CROSS JOIN constants
			WHERE (command.status IN ('pending','notified')
			       OR (command.status = 'claimed'
			           AND (command.claim_until IS NULL OR command.claim_until <= constants.clock_ms)))
			  AND command.created_at < constants.clock_ms - 30::bigint*24*60*60*1000
			ORDER BY command.created_at, command.command_id LIMIT $1
			FOR UPDATE OF command SKIP LOCKED
		)
		UPDATE agent_commands command SET status = 'expired', completed_at = constants.clock_ms,
			claim_owner_runtime_id = NULL, claim_token_hash = NULL, claim_until = NULL
		FROM target CROSS JOIN constants
		WHERE command.command_id = target.command_id
		  AND (command.status IN ('pending','notified')
		       OR (command.status = 'claimed'
		           AND (command.claim_until IS NULL OR command.claim_until <= constants.clock_ms)))`},
		{"attention_command_expiry_recovery", `WITH constants AS (
			SELECT (extract(epoch FROM clock_timestamp())*1000)::bigint AS clock_ms
		), target AS (
			SELECT item.attention_id
			FROM agent_attention_items item CROSS JOIN constants
			WHERE item.producer = 'agent'
			  AND item.status IN ('selected','pending')
			  AND (item.expires_at IS NULL OR item.expires_at >= constants.clock_ms)
			  AND EXISTS (
				SELECT 1 FROM agent_commands command
				WHERE command.agent_id = item.agent_id
				  AND command.attention_id = item.attention_id
				  AND command.command_type = 'attention_response'
				  AND command.status = 'expired'
			  )
			  AND NOT EXISTS (
				SELECT 1 FROM agent_commands command
				WHERE command.agent_id = item.agent_id
				  AND command.attention_id = item.attention_id
				  AND command.status IN ('pending','notified','claimed')
			  )
			ORDER BY item.updated_at, item.attention_id LIMIT $1
			FOR UPDATE OF item SKIP LOCKED
		)
		UPDATE agent_attention_items item
		SET status = 'open', response_status = 'failed', selected_action_key = NULL,
			updated_at = constants.clock_ms, item_revision = item.item_revision + 1
		FROM target CROSS JOIN constants
		WHERE item.attention_id = target.attention_id
		  AND item.producer = 'agent' AND item.status IN ('selected','pending')
		  AND (item.expires_at IS NULL OR item.expires_at >= constants.clock_ms)
		  AND NOT EXISTS (
			SELECT 1 FROM agent_commands command
			WHERE command.agent_id = item.agent_id
			  AND command.attention_id = item.attention_id
			  AND command.status IN ('pending','notified','claimed')
		  )`},
		{"commands", boundedDelete("agent_commands", "status IN ('completed','failed','expired') AND COALESCE(completed_at, created_at) < clock_ms() - 30*day_ms()")},
		{"control_outbox_orphans", boundedDelete("control_wakeup_outbox", `event_type = 'command_available' AND (
			status = 'pending' AND (
			NOT EXISTS (SELECT 1 FROM agent_commands command WHERE command.command_id = row.entity_id)
			OR EXISTS (SELECT 1 FROM agent_commands command WHERE command.command_id = row.entity_id
				AND command.status IN ('completed','failed','expired'))))`)},
		{"attention_command_payload_redaction", `WITH constants AS (
			SELECT (extract(epoch FROM clock_timestamp())*1000)::bigint AS clock_ms
		), target AS (
			SELECT command.command_id
			FROM agent_commands command
			JOIN agent_attention_items item ON item.attention_id = command.attention_id
			CROSS JOIN constants
			WHERE command.command_type = 'attention_response'
			  AND command.status IN ('completed','failed','expired')
			  AND item.producer = 'agent'
			  AND item.generated_at < constants.clock_ms - 7*24*60*60*1000
			  AND (COALESCE(command.payload #>> '{attention_snapshot,title}', '') <> ''
			    OR COALESCE(command.payload #>> '{attention_snapshot,body}', '') <> ''
			    OR COALESCE(command.payload #>> '{attention_snapshot,recommendation}', '') <> '')
			ORDER BY item.generated_at, command.command_id LIMIT $1
			FOR UPDATE OF command SKIP LOCKED
		)
		UPDATE agent_commands command SET payload =
			jsonb_set(
				jsonb_set(
					jsonb_set(command.payload, '{attention_snapshot,title}', '""'::jsonb, false),
					'{attention_snapshot,body}', '""'::jsonb, false),
				'{attention_snapshot,recommendation}', '""'::jsonb, false)
		FROM target WHERE command.command_id = target.command_id
		  AND command.status IN ('completed','failed','expired')`},
		{"attention_text_redaction", `WITH constants AS (
			SELECT (extract(epoch FROM clock_timestamp())*1000)::bigint AS clock_ms
		), target AS (
			SELECT item.attention_id FROM agent_attention_items item CROSS JOIN constants
			WHERE item.producer = 'agent' AND item.redacted_at IS NULL
			  AND item.generated_at < constants.clock_ms - 7*24*60*60*1000
			ORDER BY item.generated_at, item.attention_id LIMIT $1
			FOR UPDATE OF item SKIP LOCKED
		)
		UPDATE agent_attention_items item SET title = '', summary = '', body = '', recommendation = '',
			redacted_at = constants.clock_ms, updated_at = constants.clock_ms
		FROM target CROSS JOIN constants WHERE item.attention_id = target.attention_id`},
		{"attention_expiry", `WITH constants AS (
			SELECT (extract(epoch FROM clock_timestamp())*1000)::bigint AS clock_ms
		), target AS (
			SELECT item.attention_id FROM agent_attention_items item CROSS JOIN constants
			WHERE item.producer = 'agent' AND item.status IN ('open','selected','pending') AND item.expires_at IS NOT NULL
			  AND item.expires_at < constants.clock_ms
			  AND NOT EXISTS (
				SELECT 1 FROM agent_commands command
				WHERE command.agent_id = item.agent_id AND command.attention_id = item.attention_id
				  AND command.status IN ('pending','notified','claimed')
			  )
			ORDER BY item.expires_at, item.attention_id LIMIT $1
			FOR UPDATE OF item SKIP LOCKED
		)
		UPDATE agent_attention_items item SET status = 'expired',
			response_status = CASE WHEN item.status IN ('selected','pending') THEN 'failed' ELSE item.response_status END,
			updated_at = constants.clock_ms, item_revision = item.item_revision + 1
		FROM target CROSS JOIN constants WHERE item.attention_id = target.attention_id
		  AND item.producer = 'agent' AND item.status IN ('open','selected','pending')
		  AND item.expires_at IS NOT NULL AND item.expires_at < constants.clock_ms
		  AND NOT EXISTS (
			SELECT 1 FROM agent_commands command
			WHERE command.agent_id = item.agent_id AND command.attention_id = item.attention_id
			  AND command.status IN ('pending','notified','claimed')
		  )`},
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
		WHERE %s ORDER BY row.ctid LIMIT $1 FOR UPDATE OF row SKIP LOCKED
	)
	DELETE FROM %s row USING target WHERE row.ctid = target.ctid`, DayMS, table, predicate, table)
}
