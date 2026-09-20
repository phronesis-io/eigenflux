package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
)

type goalRepair struct {
	agentID int64
	goalID  int64
	bio     string
	oldText string
	newText string
}

// Both original migration provenance and exact text equality are required.
// Re-saving an unchanged template through the editor can set human_edit; a
// genuinely edited goal or a newly prefilled goal must not qualify.
const repairCandidatesSQL = `SELECT g.agent_id, g.goal_id,
	COALESCE(d.draft_data->'identity_card'->>'bio', ''), g.goal_text
	FROM agent_network_goals g
	JOIN LATERAL (
		SELECT draft_data FROM agent_onboarding_drafts
		WHERE agent_id = g.agent_id AND request_id = 'legacy-backfill-v2'
		  AND actor_type = 'system_derived'
		ORDER BY revision LIMIT 1
	) d ON g.goal_text = d.draft_data->>'network_goal'
	JOIN agent_onboarding_v2 o ON o.agent_id = g.agent_id AND o.state = 'completed'
	WHERE g.status = 'active' AND g.source IN ('system_derived', 'human_edit')
	  AND g.agent_id > $1 AND ($2::bigint = 0 OR g.agent_id = $2)
	ORDER BY g.agent_id LIMIT $3`

func loadGoalRepairs(db *sql.DB, cursor, agentID int64, limit int) ([]goalRepair, int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, repairCandidatesSQL, cursor, agentID, limit)
	if err != nil {
		return nil, cursor, err
	}
	defer rows.Close()
	var repairs []goalRepair
	for rows.Next() {
		var row goalRepair
		if err := rows.Scan(&row.agentID, &row.goalID, &row.bio, &row.oldText); err != nil {
			return nil, cursor, err
		}
		cursor = row.agentID
		agent := legacyAgent{bio: row.bio}
		row.newText = legacyNetworkGoal(agent)
		if strings.TrimSpace(row.bio) != "" && row.oldText == originalLegacyNetworkGoal(agent) && row.newText != row.oldText {
			repairs = append(repairs, row)
		}
	}
	return repairs, cursor, rows.Err()
}

func repairLegacyGoals(db *sql.DB, apply bool, batchSize int, agentID int64) error {
	var cursor int64
	total, changed := 0, 0
	for {
		repairs, next, err := loadGoalRepairs(db, cursor, agentID, batchSize)
		if err != nil {
			return err
		}
		if next == cursor {
			break
		}
		cursor = next
		total += len(repairs)
		if apply {
			for _, repair := range repairs {
				updated, err := applyGoalRepair(db, repair, time.Now().UnixMilli())
				if err != nil {
					return fmt.Errorf("agent %d: %w", repair.agentID, err)
				}
				if updated {
					changed++
				}
			}
		}
		log.Printf("goal repair: apply=%t eligible=%d repaired=%d last_agent_id=%d", apply, total, changed, cursor)
	}
	log.Printf("goal repair complete: apply=%t eligible=%d repaired=%d", apply, total, changed)
	return nil
}

func applyGoalRepair(db *sql.DB, repair goalRepair, now int64) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SET LOCAL lock_timeout = '2s'; SET LOCAL statement_timeout = '10s'`); err != nil {
		return false, err
	}
	// Match the editor's head-first lock order and recheck after acquiring it.
	var current, active int64
	err = tx.QueryRowContext(ctx, `SELECT current_revision, active_revision
		FROM agent_context_heads WHERE agent_id = $1 AND active_revision IS NOT NULL FOR UPDATE`,
		repair.agentID).Scan(&current, &active)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var goalText string
	var goalVersion int64
	err = tx.QueryRowContext(ctx, `SELECT goal_text, version FROM agent_network_goals
		WHERE agent_id = $1 AND goal_id = $2 AND status = 'active'
		  AND source IN ('system_derived', 'human_edit') FOR UPDATE`, repair.agentID, repair.goalID).Scan(&goalText, &goalVersion)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && goalText != repair.oldText) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var coherent bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM agent_context_revisions r JOIN agent_onboarding_v2 o USING (agent_id)
		WHERE r.agent_id = $1 AND r.revision = $2 AND o.state = 'completed'
		  AND o.active_context_revision = $2
		  AND r.compiled_context->'network_goal'->>'goal_id' = $3::bigint::text
		  AND r.compiled_context->'network_goal'->>'text' = $4
	)`, repair.agentID, active, repair.goalID, repair.oldText).Scan(&coherent); err != nil {
		return false, err
	}
	if !coherent {
		return false, fmt.Errorf("active goal/context/onboarding mismatch; no changes committed")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_network_goals SET status = 'deleted', updated_at = $1
		WHERE goal_id = $2`, now, repair.goalID); err != nil {
		return false, err
	}
	var newGoalID int64
	if err := tx.QueryRowContext(ctx, `INSERT INTO agent_network_goals
		(agent_id, goal_text, source, status, version, created_at, updated_at)
		VALUES ($1, $2, 'system_derived', 'active', $4, $3, $3) RETURNING goal_id`,
		repair.agentID, repair.newText, now, goalVersion+1).Scan(&newGoalID); err != nil {
		return false, err
	}
	// Clone the active immutable snapshot so unrelated settings, intent actions,
	// and any future context fields are preserved exactly.
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_context_revisions
		(agent_id, revision, compiled_context, schema_version, generated_at)
		SELECT agent_id, $3, jsonb_set(jsonb_set(compiled_context,
		  '{network_goal}', jsonb_build_object('goal_id', $4::bigint::text,
		    'text', $5::text, 'source', 'system_derived', 'status', 'active')),
		  '{context_revision}', to_jsonb($3::bigint)), schema_version, $6
		FROM agent_context_revisions WHERE agent_id = $1 AND revision = $2`,
		repair.agentID, active, current+1, newGoalID, repair.newText, now); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_context_heads
		SET current_revision = $2, active_revision = $2, updated_at = $3 WHERE agent_id = $1`,
		repair.agentID, current+1, now); err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_onboarding_v2 SET active_context_revision = $2, updated_at = $3
		WHERE agent_id = $1 AND state = 'completed' AND active_context_revision = $4`,
		repair.agentID, current+1, now, active)
	if err != nil {
		return false, err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return false, fmt.Errorf("onboarding changed during repair: affected=%d err=%v", n, err)
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}
