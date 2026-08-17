// Command console_v2_backfill prepares legacy Agents for Console V2 without
// changing their V1 authentication behavior.
//
// It is dry-run by default. Writes require --apply. Applied runs are cursor
// based, bounded, idempotent, and safe to resume after interruption:
//
//	PG_DSN=... go run ./scripts/console_v2_backfill
//	PG_DSN=... go run ./scripts/console_v2_backfill --apply --batch-size=500
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"log"
	"os"
	"strings"
	"time"

	_ "github.com/lib/pq"
)

const jobName = "console_v2_legacy_agents_v2"

type legacyAgent struct {
	agentID          int64
	email            string
	agentName        string
	bio              string
	recurringPublish bool
	autoReplyPM      bool
	autoComment      bool
	showAddFriend    bool
}

func main() {
	apply := flag.Bool("apply", false, "write the backfill; without this flag the command is read-only")
	batchSize := flag.Int("batch-size", 500, "agents per transaction (1-5000)")
	flag.Parse()

	if *batchSize < 1 || *batchSize > 5000 {
		log.Fatal("--batch-size must be between 1 and 5000")
	}
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		log.Fatal("PG_DSN is required")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(5 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	if err := db.PingContext(ctx); err != nil {
		cancel()
		log.Fatalf("ping database: %v", err)
	}
	cancel()

	duplicateCount, invalidCount, reservedCount, eligibleCount, err := inspect(db)
	if err != nil {
		log.Fatalf("preflight: %v", err)
	}
	log.Printf("preflight: normalized_duplicates=%d invalid_emails=%d reserved_aliases=%d eligible_legacy_agents=%d",
		duplicateCount, invalidCount, reservedCount, eligibleCount)
	if !*apply {
		log.Println("dry-run complete; pass --apply to write")
		return
	}

	if err := recordConflicts(db); err != nil {
		log.Fatalf("record conflicts: %v", err)
	}
	for {
		processed, lastAgentID, done, err := applyBatch(db, *batchSize)
		if err != nil {
			log.Fatalf("backfill batch: %v", err)
		}
		if processed > 0 {
			log.Printf("committed batch: processed=%d last_agent_id=%d", processed, lastAgentID)
		}
		if done {
			log.Println("backfill complete")
			return
		}
	}
}

func inspect(db *sql.DB) (duplicates, invalid, reserved, eligible int64, err error) {
	queries := []struct {
		dst   *int64
		query string
	}{
		{&duplicates, `SELECT COUNT(*) FROM (
			SELECT lower(btrim(email)) FROM agents WHERE email_kind = 'legacy_real'
			GROUP BY lower(btrim(email)) HAVING COUNT(*) > 1
		) conflicts`},
		{&invalid, `SELECT COUNT(*) FROM agents
			WHERE email_kind = 'legacy_real' AND lower(btrim(email)) !~
			'^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+[.][a-zA-Z]{2,}$'`},
		{&reserved, `SELECT COUNT(*) FROM agents
			WHERE email_kind = 'legacy_real' AND lower(btrim(email)) LIKE '%@identity.invalid'`},
		{&eligible, `SELECT COUNT(*) FROM agents a
			WHERE a.email_kind = 'legacy_real'
			  AND lower(btrim(a.email)) ~ '^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+[.][a-zA-Z]{2,}$'
			  AND lower(btrim(a.email)) NOT LIKE '%@identity.invalid'
			  AND NOT EXISTS (
			      SELECT 1 FROM agents other
			      WHERE other.email_kind = 'legacy_real'
			        AND lower(btrim(other.email)) = lower(btrim(a.email))
			        AND other.agent_id <> a.agent_id)`},
	}
	for _, item := range queries {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		err = db.QueryRowContext(ctx, item.query).Scan(item.dst)
		cancel()
		if err != nil {
			return 0, 0, 0, 0, err
		}
	}
	return duplicates, invalid, reserved, eligible, nil
}

func recordConflicts(db *sql.DB) error {
	now := time.Now().UnixMilli()
	statements := []string{
		`INSERT INTO console_v2_email_conflicts
			(normalized_email, agent_ids, reason, detected_at)
		 SELECT lower(btrim(email)), jsonb_agg(agent_id ORDER BY agent_id),
			'normalized_duplicate', $1
		 FROM agents WHERE email_kind = 'legacy_real'
		 GROUP BY lower(btrim(email)) HAVING COUNT(*) > 1
		 ON CONFLICT (normalized_email) DO UPDATE SET
			agent_ids = EXCLUDED.agent_ids, reason = EXCLUDED.reason,
			detected_at = EXCLUDED.detected_at, resolved_at = NULL`,
		`INSERT INTO console_v2_email_conflicts
			(normalized_email, agent_ids, reason, detected_at)
		 SELECT lower(btrim(email)), jsonb_agg(agent_id ORDER BY agent_id),
			'invalid_email', $1
		 FROM agents
		 WHERE email_kind = 'legacy_real' AND lower(btrim(email)) !~
			'^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+[.][a-zA-Z]{2,}$'
		 GROUP BY lower(btrim(email))
		 ON CONFLICT (normalized_email) DO NOTHING`,
		`INSERT INTO console_v2_email_conflicts
			(normalized_email, agent_ids, reason, detected_at)
		 SELECT lower(btrim(email)), jsonb_agg(agent_id ORDER BY agent_id),
			'reserved_internal_alias', $1
		 FROM agents
		 WHERE email_kind = 'legacy_real' AND lower(btrim(email)) LIKE '%@identity.invalid'
		 GROUP BY lower(btrim(email))
		 ON CONFLICT (normalized_email) DO NOTHING`,
	}
	for _, statement := range statements {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		_, err := db.ExecContext(ctx, statement, now)
		cancel()
		if err != nil {
			return err
		}
	}
	return nil
}

func applyBatch(db *sql.DB, batchSize int) (processed, lastAgentID int64, done bool, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return 0, 0, false, err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UnixMilli()
	if _, err := tx.ExecContext(ctx, `INSERT INTO console_v2_backfill_state
		(job_name, last_agent_id, processed_count, conflict_count, updated_at)
		VALUES ($1, 0, 0, 0, $2) ON CONFLICT (job_name) DO NOTHING`, jobName, now); err != nil {
		return 0, 0, false, err
	}
	var cursor int64
	if err := tx.QueryRowContext(ctx, `SELECT last_agent_id FROM console_v2_backfill_state
		WHERE job_name = $1 FOR UPDATE`, jobName).Scan(&cursor); err != nil {
		return 0, 0, false, err
	}

	rows, err := tx.QueryContext(ctx, `SELECT a.agent_id, a.email, a.agent_name, COALESCE(a.bio, ''),
		COALESCE(s.recurring_publish, true), COALESCE(s.auto_reply_pm, true),
		COALESCE(s.auto_comment, true), COALESCE(s.show_add_friend, true)
		FROM agents a LEFT JOIN agent_settings s ON s.agent_id = a.agent_id
		WHERE a.email_kind = 'legacy_real' AND a.agent_id > $1
		ORDER BY a.agent_id LIMIT $2`, cursor, batchSize)
	if err != nil {
		return 0, 0, false, err
	}
	var agents []legacyAgent
	for rows.Next() {
		var agent legacyAgent
		if err := rows.Scan(&agent.agentID, &agent.email, &agent.agentName, &agent.bio,
			&agent.recurringPublish, &agent.autoReplyPM, &agent.autoComment, &agent.showAddFriend); err != nil {
			_ = rows.Close()
			return 0, 0, false, err
		}
		agents = append(agents, agent)
	}
	if err := rows.Close(); err != nil {
		return 0, 0, false, err
	}
	if len(agents) == 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE console_v2_backfill_state SET completed_at = $1, updated_at = $1,
			conflict_count = (SELECT COUNT(*) FROM console_v2_email_conflicts WHERE resolved_at IS NULL)
			WHERE job_name = $2`, now, jobName); err != nil {
			return 0, 0, false, err
		}
		if err := tx.Commit(); err != nil {
			return 0, 0, false, err
		}
		return 0, cursor, true, nil
	}

	if err := prepareLegacyAgents(ctx, tx, agents, now); err != nil {
		return 0, 0, false, err
	}
	for _, agent := range agents {
		lastAgentID = agent.agentID
		processed++
	}
	if _, err := tx.ExecContext(ctx, `UPDATE console_v2_backfill_state
		SET last_agent_id = $1, processed_count = processed_count + $2,
			conflict_count = (SELECT COUNT(*) FROM console_v2_email_conflicts WHERE resolved_at IS NULL),
			completed_at = NULL, updated_at = $3 WHERE job_name = $4`,
		lastAgentID, processed, now, jobName); err != nil {
		return 0, 0, false, err
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, false, err
	}
	return processed, lastAgentID, false, nil
}

func prepareLegacyAgent(ctx context.Context, tx *sql.Tx, agent legacyAgent, now int64) error {
	return prepareLegacyAgents(ctx, tx, []legacyAgent{agent}, now)
}

type legacyBackfillRow struct {
	AgentID          int64  `json:"agent_id"`
	Email            string `json:"email"`
	AgentName        string `json:"agent_name"`
	Bio              string `json:"bio"`
	NetworkGoal      string `json:"network_goal"`
	RecurringPublish bool   `json:"recurring_publish"`
	AutoReplyPM      bool   `json:"auto_reply_pm"`
	AutoComment      bool   `json:"auto_comment"`
	ShowAddFriend    bool   `json:"show_add_friend"`
}

func legacyNetworkGoal(agent legacyAgent) string {
	bio := strings.TrimSpace(agent.bio)
	if bio == "" {
		return "延续现有 EigenFlux 网络活动与合作"
	}
	runes := []rune(bio)
	if len(runes) > 240 {
		bio = string(runes[:240])
	}
	return "延续现有 Agent 方向：" + bio
}

// prepareLegacyAgents uses a fixed number of set-based SQL statements for a
// whole page (default 500, max 5000). It creates canonical goal + immutable
// empty-intent context before marking legacy onboarding complete.
func prepareLegacyAgents(ctx context.Context, tx *sql.Tx, agents []legacyAgent, now int64) error {
	rows := make([]legacyBackfillRow, 0, len(agents))
	for _, agent := range agents {
		rows = append(rows, legacyBackfillRow{
			AgentID: agent.agentID, Email: agent.email, AgentName: agent.agentName, Bio: agent.bio,
			NetworkGoal: legacyNetworkGoal(agent), RecurringPublish: agent.recurringPublish,
			AutoReplyPM: agent.autoReplyPM, AutoComment: agent.autoComment, ShowAddFriend: agent.showAddFriend,
		})
	}
	encoded, err := json.Marshal(rows)
	if err != nil {
		return err
	}
	batch := string(encoded)
	statements := []string{
		`WITH source AS (SELECT * FROM jsonb_to_recordset($1::jsonb) AS row(agent_id bigint))
		 UPDATE agent_cards card SET public_card_version = card.card_version,
		   public_card_generated_at = card.generated_at
		 FROM source WHERE card.agent_id = source.agent_id
		   AND $2::bigint >= 0
		   AND (card.public_card_version <> card.card_version
		        OR card.public_card_generated_at <> card.generated_at)`,
		`WITH source AS (SELECT * FROM jsonb_to_recordset($1::jsonb) AS row(agent_id bigint, email text))
		 INSERT INTO agent_email_bindings
		 (agent_id, normalized_email, normalization_version, verification_state, status, created_at, updated_at)
		 SELECT source.agent_id, lower(btrim(source.email)), 1, 'legacy_unverified', 'active', $2, $2 FROM source
		 WHERE lower(btrim(source.email)) ~ '^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+[.][a-zA-Z]{2,}$'
		   AND lower(btrim(source.email)) NOT LIKE '%@identity.invalid'
		   AND NOT EXISTS (SELECT 1 FROM console_v2_email_conflicts conflict
		                   WHERE conflict.normalized_email = lower(btrim(source.email)) AND conflict.resolved_at IS NULL)
		 ON CONFLICT DO NOTHING`,
		`WITH source AS (SELECT * FROM jsonb_to_recordset($1::jsonb) AS row(agent_id bigint))
		 INSERT INTO agent_context_heads (agent_id, current_revision, updated_at)
		 SELECT agent_id, 0, $2 FROM source ON CONFLICT (agent_id) DO NOTHING`,
		`WITH source AS (SELECT * FROM jsonb_to_recordset($1::jsonb) AS row(agent_id bigint, network_goal text))
		 INSERT INTO agent_network_goals (agent_id, goal_text, source, status, version, created_at, updated_at)
		 SELECT agent_id, network_goal, 'system_derived', 'active', 1, $2, $2 FROM source
		 ON CONFLICT (agent_id) WHERE status = 'active' DO NOTHING`,
		`WITH source AS (SELECT * FROM jsonb_to_recordset($1::jsonb) AS row(agent_id bigint))
		 SELECT pg_advisory_xact_lock(source.agent_id) FROM source
		 WHERE $2::bigint >= 0 ORDER BY source.agent_id`,
		`WITH source AS (SELECT * FROM jsonb_to_recordset($1::jsonb) AS row(
		   agent_id bigint, recurring_publish boolean, auto_reply_pm boolean,
		   auto_comment boolean, show_add_friend boolean)),
		 candidate AS (
		   SELECT head.agent_id, head.current_revision + 1 AS revision, goal.goal_id, goal.goal_text,
		          goal.source, goal.status, source.recurring_publish, source.auto_reply_pm,
		          source.auto_comment, source.show_add_friend FROM source
		   JOIN agent_context_heads head ON head.agent_id = source.agent_id AND head.active_revision IS NULL
		   JOIN agent_network_goals goal ON goal.agent_id = source.agent_id AND goal.status = 'active'
		 )
		 INSERT INTO agent_context_revisions (agent_id, revision, compiled_context, schema_version, generated_at)
		 SELECT agent_id, revision, jsonb_build_object(
		   'context_revision', revision,
		   'network_goal', jsonb_build_object('goal_id', goal_id::text, 'text', goal_text, 'source', source, 'status', status),
		   'intent_actions', '[]'::jsonb,
		   'security_boundary', jsonb_build_object('recurring_publish', recurring_publish,
		     'auto_reply_pm', auto_reply_pm, 'auto_comment', auto_comment,
		     'show_add_friend', show_add_friend),
		   'safety', jsonb_build_object('external_side_effects', 'require_user_confirmation')
		 ), 1, $2 FROM candidate ON CONFLICT DO NOTHING`,
		`WITH source AS (SELECT * FROM jsonb_to_recordset($1::jsonb) AS row(agent_id bigint))
		 UPDATE agent_context_heads head SET current_revision = head.current_revision + 1,
		   active_revision = head.current_revision + 1, updated_at = $2
		 FROM source WHERE head.agent_id = source.agent_id AND head.active_revision IS NULL
		   AND EXISTS (SELECT 1 FROM agent_context_revisions revision
		               WHERE revision.agent_id = head.agent_id AND revision.revision = head.current_revision + 1)`,
		`WITH source AS (SELECT * FROM jsonb_to_recordset($1::jsonb) AS row(agent_id bigint))
		 INSERT INTO agent_onboarding_v2
		 (agent_id, state, current_step, revision, completed_at, active_context_revision, created_at, updated_at)
		 SELECT source.agent_id, 'completed', 5, 1, $2, head.active_revision, $2, $2 FROM source
		 JOIN agent_context_heads head ON head.agent_id = source.agent_id AND head.active_revision IS NOT NULL
		 ON CONFLICT (agent_id) DO UPDATE SET state = 'completed', current_step = 5,
		   revision = agent_onboarding_v2.revision + 1, completed_at = $2,
		   active_context_revision = EXCLUDED.active_context_revision, updated_at = $2
		 WHERE agent_onboarding_v2.state = 'migration_pending'`,
		`WITH source AS (SELECT * FROM jsonb_to_recordset($1::jsonb) AS row(agent_id bigint))
		 INSERT INTO agent_feed_v2_settings (agent_id, poll_interval_seconds, explicitly_set, updated_at)
		 SELECT agent_id, 600, false, $2 FROM source ON CONFLICT (agent_id) DO NOTHING`,
		`WITH source AS (SELECT * FROM jsonb_to_recordset($1::jsonb) AS row(
		 agent_id bigint, agent_name text, bio text, network_goal text,
		 recurring_publish boolean, auto_reply_pm boolean, auto_comment boolean, show_add_friend boolean))
		 INSERT INTO agent_onboarding_drafts
		 (agent_id, revision, draft_data, field_provenance, actor_type, request_id, created_at)
		 SELECT agent_id, 1, jsonb_build_object(
		   'identity_card', jsonb_build_object('agent_name', agent_name, 'bio', bio),
		   'security_boundary', jsonb_build_object('recurring_publish', recurring_publish,
		     'auto_reply_pm', auto_reply_pm, 'auto_comment', auto_comment, 'show_add_friend', show_add_friend),
		   'network_goal', network_goal, 'intent_actions', '[]'::jsonb),
		   '{"identity_card":"legacy_migration","security_boundary":"legacy_migration","network_goal":"legacy_migration","intent_actions":"legacy_migration"}'::jsonb,
		   'system_derived', 'legacy-backfill-v2', $2 FROM source ON CONFLICT DO NOTHING`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement, batch, now); err != nil {
			return err
		}
	}
	return nil
}
