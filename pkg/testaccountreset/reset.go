// Package testaccountreset removes one fixed-OTP test account so the same email
// registers as a brand-new agent on its next login.
//
// Only addresses matched by a full-address OFFICIAL_TEST_EMAIL_SUFFIXES pattern
// are eligible: that list is the deployment's own definition of a test account.
// An entry qualifies only when it is a literal address whose sole wildcards are
// single-digit classes such as [0-9] after a literal prefix. "@domain" entries
// and every other glob ("*", "?", letter or negated classes) are ignored on
// purpose: the PGC publishing fleet shares the test domain, and an open-ended
// pattern could reach it.
package testaccountreset

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"eigenflux_server/pkg/config"
)

// ErrHasTrades is returned on apply when the account has orders or listings.
var ErrHasTrades = errors.New("account has trading rows that involve a counterparty or payment receipts; settle or remove them by hand before resetting")

// narrowPattern is a literal local-part prefix of at least three characters,
// then only single-digit classes, then a literal domain.
var narrowPattern = regexp.MustCompile(`^[a-z0-9._+-]{3,}(\[[0-9]-[0-9]\])*@[a-z0-9.-]+$`)

// ErrNotTestAccount is returned for any email outside the test-account patterns.
var ErrNotTestAccount = errors.New("not a test account: needs a PGC_EMAIL_SUFFIXES domain and a full-address OFFICIAL_TEST_EMAIL_SUFFIXES pattern")

// ErrOfficial is returned for the official account, whatever its address.
var ErrOfficial = errors.New("agent is flagged is_official")

// Guard is the deployment's definition of a resettable address.
type Guard struct {
	TestPatterns     []string // OFFICIAL_TEST_EMAIL_SUFFIXES
	InternalSuffixes []string // PGC_EMAIL_SUFFIXES: domains the company controls
}

// Allowed reports whether email is a resettable test account: it must sit on a
// company-controlled domain, where no real user can own an address, and match a
// narrow test-account pattern, which keeps the PGC fleet on that domain out.
func (g Guard) Allowed(email string) bool {
	email = NormalizeEmail(email)
	return config.EmailMatchesAnySuffix(email, g.InternalSuffixes) && allowedByPattern(email, g.TestPatterns)
}

func allowedByPattern(email string, patterns []string) bool {
	full := make([]string, 0, len(patterns))
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if !narrowPattern.MatchString(p) {
			continue
		}
		full = append(full, p)
	}
	return len(full) > 0 && config.EmailMatchesAnyPattern(email, full)
}

// NormalizeEmail mirrors the auth service's login normalization.
func NormalizeEmail(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// Step is one table the reset touches. Where has a single parameter, $1: the
// agent id, or the normalized email when ByEmail is set.
type Step struct {
	Table   string
	Where   string
	ByEmail bool
}

const (
	ownPrincipals = "(SELECT principal_id FROM agent_principals WHERE agent_id = $1)"
	ownSessions   = "(SELECT session_id FROM console_v2_sessions WHERE agent_id = $1)"
	ownChallenges = "(SELECT challenge_id FROM v2_email_challenges WHERE subject_agent_id = $1)"
	ownItems      = "(SELECT item_id FROM raw_items WHERE author_agent_id = $1)"
)

// Steps run in order, before the agents row is deleted. The first four tables
// reference agents without ON DELETE CASCADE and would block the delete; the
// rest carry an agent id with no foreign key and would survive it. Every other
// agent-scoped table cascades from agents.
var Steps = []Step{
	{Table: "agent_account_recovery_audit", Where: "source_agent_id = $1 OR target_agent_id = $1"},
	{Table: "agent_account_recoveries", Where: "source_agent_id = $1 OR target_agent_id = $1 OR principal_id IN " + ownPrincipals +
		" OR console_session_id IN " + ownSessions + " OR email_challenge_id IN " + ownChallenges},
	{Table: "agent_cli_account_switch_audit", Where: "source_agent_id = $1 OR target_agent_id = $1"},
	{Table: "agent_cli_account_switches", Where: "source_agent_id = $1 OR target_agent_id = $1 OR principal_id IN " + ownPrincipals +
		" OR source_console_session_id IN " + ownSessions + " OR target_console_session_id IN " + ownSessions},

	{Table: "processed_items", Where: "item_id IN " + ownItems},
	{Table: "raw_items", Where: "author_agent_id = $1"},
	{Table: "conversations", Where: "participant_a = $1 OR participant_b = $1"},
	{Table: "user_relations", Where: "from_uid = $1 OR to_uid = $1"},
	{Table: "friend_requests", Where: "from_uid = $1 OR to_uid = $1"},
	{Table: "replay_logs", Where: "agent_id = $1"},
	{Table: "feedback_logs", Where: "agent_id = $1"},
	{Table: "followup_labels", Where: "agent_id = $1"},
	{Table: "agent_activity_log", Where: "agent_id = $1"},
	{Table: "agent_bio_history", Where: "agent_id = $1"},
	{Table: "agent_settings", Where: "agent_id = $1"},
	{Table: "agent_sessions", Where: "agent_id = $1"},
	{Table: "agent_profiles", Where: "agent_id = $1"},
	{Table: "invite_codes", Where: "agent_id = $1"},
	{Table: "auth_email_challenges", Where: "email = $1", ByEmail: true},
	{Table: "console_v2_email_conflicts", Where: "normalized_email = $1", ByEmail: true},
}

// Blocking tables are never deleted, and any row in them refuses the reset:
// orders and listings involve a counterparty and payment receipts.
var Blocking = []Step{
	{Table: "trading_services", Where: "seller_agent_id = $1"},
	{Table: "trade_orders", Where: "buyer_agent_id = $1 OR seller_agent_id = $1"},
	{Table: "trade_order_events", Where: "actor_agent_id = $1"},
}

// ReportOnly rows are counted and left alone: other agents that registered
// through this account's invite keep pointing at the old agent id.
var ReportOnly = []Step{
	{Table: "agents", Where: "inviter_agent_id = $1"},
}

// indirect rows disappear through a cascade that does not start at agents.
var indirect = []Step{
	{Table: "private_messages", Where: "conv_id IN (SELECT conv_id FROM conversations WHERE participant_a = $1 OR participant_b = $1)"},
	{Table: "conversation_topic_events", Where: "conv_id IN (SELECT conv_id FROM conversations WHERE participant_a = $1 OR participant_b = $1)"},
}

// Count is a table and the number of rows matched (dry run) or deleted (apply).
type Count struct {
	Table string
	Rows  int64
}

// Report describes one reset.
type Report struct {
	Email       string
	AgentID     int64 // 0 when the email has no agent row
	Applied     bool
	Deleted     []Count  // Steps, then "agents"
	Cascaded    []Count  // rows removed by ON DELETE CASCADE from agents (counted before the delete)
	Blocked     []Count  // Blocking tables with rows; apply refuses while any exist
	LeftInPlace []Count  // ReportOnly tables with rows
	TokenHashes []string // V1 session hashes, for the auth:session:* Redis keys
	Peers       []int64  // agents sharing a relation or conversation; their relation caches go stale
	Convs       []Conv   // conversations deleted with the account
}

// Conv identifies a deleted conversation's Redis caches.
type Conv struct{ ID, A, B, Origin int64 }

// ResetPostgres deletes the account's rows in one transaction. With apply=false
// it runs the same statements as counts and changes nothing.
func ResetPostgres(ctx context.Context, db *sql.DB, email string, guard Guard, apply bool) (*Report, error) {
	email = NormalizeEmail(email)
	if !guard.Allowed(email) {
		return nil, fmt.Errorf("%q: %w", email, ErrNotTestAccount)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	report := &Report{Email: email, Applied: apply}
	var official bool
	err = tx.QueryRowContext(ctx, `SELECT agent_id, is_official FROM agents WHERE email = $1 FOR UPDATE`, email).Scan(&report.AgentID, &official)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("look up agent: %w", err)
	}
	if official {
		return nil, fmt.Errorf("%q: %w", email, ErrOfficial)
	}

	if report.AgentID != 0 {
		if report.TokenHashes, err = tokenHashes(ctx, tx, report.AgentID); err != nil {
			return nil, err
		}
		if report.Cascaded, err = cascadeCounts(ctx, tx, report.AgentID); err != nil {
			return nil, err
		}
		for _, group := range []struct {
			steps []Step
			into  *[]Count
		}{{indirect, &report.Cascaded}, {Blocking, &report.Blocked}, {ReportOnly, &report.LeftInPlace}} {
			for _, step := range group.steps {
				n, err := run(ctx, tx, step, report.AgentID, email, false)
				if err != nil {
					return nil, err
				}
				if n > 0 {
					*group.into = append(*group.into, Count{step.Table, n})
				}
			}
		}
		if report.Peers, report.Convs, err = peers(ctx, tx, report.AgentID); err != nil {
			return nil, err
		}
		if apply && len(report.Blocked) > 0 {
			return report, ErrHasTrades
		}
	}
	steps := append(append([]Step{}, Steps...), Step{Table: "agents", Where: "agent_id = $1"})
	for _, step := range steps {
		n, err := run(ctx, tx, step, report.AgentID, email, apply)
		if err != nil {
			return nil, err
		}
		report.Deleted = append(report.Deleted, Count{step.Table, n})
	}
	if !apply {
		return report, nil
	}
	return report, tx.Commit()
}

func run(ctx context.Context, tx *sql.Tx, step Step, agentID int64, email string, apply bool) (int64, error) {
	var arg any = agentID
	if step.ByEmail {
		arg = email
	} else if agentID == 0 {
		return 0, nil
	}
	if !apply {
		var n int64
		err := tx.QueryRowContext(ctx, "SELECT count(*) FROM "+step.Table+" WHERE "+step.Where, arg).Scan(&n)
		if err != nil {
			return 0, fmt.Errorf("count %s: %w", step.Table, err)
		}
		return n, nil
	}
	res, err := tx.ExecContext(ctx, "DELETE FROM "+step.Table+" WHERE "+step.Where, arg)
	if err != nil {
		return 0, fmt.Errorf("delete %s: %w", step.Table, err)
	}
	return res.RowsAffected()
}

func peers(ctx context.Context, tx *sql.Tx, agentID int64) ([]int64, []Conv, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT to_uid FROM user_relations WHERE from_uid = $1
		UNION SELECT from_uid FROM user_relations WHERE to_uid = $1
		UNION SELECT to_uid FROM friend_requests WHERE from_uid = $1
		UNION SELECT from_uid FROM friend_requests WHERE to_uid = $1`, agentID)
	if err != nil {
		return nil, nil, fmt.Errorf("list peers: %w", err)
	}
	seen := map[int64]bool{agentID: true}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, nil, err
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT conv_id, participant_a, participant_b, COALESCE(origin_id, 0) FROM conversations WHERE participant_a = $1 OR participant_b = $1`, agentID)
	if err != nil {
		return nil, nil, fmt.Errorf("list conversations: %w", err)
	}
	defer rows.Close()
	var convs []Conv
	for rows.Next() {
		var c Conv
		if err := rows.Scan(&c.ID, &c.A, &c.B, &c.Origin); err != nil {
			return nil, nil, err
		}
		convs = append(convs, c)
		for _, id := range []int64{c.A, c.B} {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	return ids, convs, rows.Err()
}

func tokenHashes(ctx context.Context, tx *sql.Tx, agentID int64) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT token_hash FROM agent_sessions WHERE agent_id = $1`, agentID)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()
	var hashes []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		hashes = append(hashes, h)
	}
	return hashes, rows.Err()
}

// cascadeCounts reads the live schema, so tables added after this package was
// written still show up in the report.
func cascadeCounts(ctx context.Context, tx *sql.Tx, agentID int64) ([]Count, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT c.conrelid::regclass::text, a.attname
		  FROM pg_constraint c
		  JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = c.conkey[1]
		 WHERE c.contype = 'f' AND c.confdeltype = 'c'
		   AND c.confrelid = 'agents'::regclass AND cardinality(c.conkey) = 1
		 ORDER BY 1, 2`)
	if err != nil {
		return nil, fmt.Errorf("list cascading tables: %w", err)
	}
	type ref struct{ table, column string }
	var refs []ref
	for rows.Next() {
		var r ref
		if err := rows.Scan(&r.table, &r.column); err != nil {
			rows.Close()
			return nil, err
		}
		refs = append(refs, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var counts []Count
	for _, r := range refs {
		var n int64
		q := fmt.Sprintf(`SELECT count(*) FROM %s WHERE %s = $1`, r.table, r.column)
		if err := tx.QueryRowContext(ctx, q, agentID).Scan(&n); err != nil {
			return nil, fmt.Errorf("count %s: %w", r.table, err)
		}
		if n > 0 {
			counts = append(counts, Count{r.table, n})
		}
	}
	return counts, nil
}

// RedisKeys lists the plain keys to DEL for a reset account.
func RedisKeys(report *Report, recallNamespace string) []string {
	sum := sha256.Sum256([]byte(report.Email))
	keys := []string{"auth:login:email:active:" + hex.EncodeToString(sum[:])}
	for _, h := range report.TokenHashes {
		keys = append(keys, "auth:session:"+h)
	}
	if report.AgentID == 0 {
		return keys
	}
	id := report.AgentID
	for _, format := range []string{
		"impr:agent:%d:items", "impr:agent:%d:groups", "impr:agent:%d:urls",
		"feed:cache:%d", "cache:profile:%d", "cache:profile:emb:%d", "cache:agent_influence:%d",
		"pm:fetch:%d", "pm:notify:%d", "milestone:notify:%d", "block:%d",
		"official:welcomed:%d", "official:firstbroadcast:%d",
		"stats:agent:%d:impressions", "stats:agent:%d:worth",
		"agentcard:la:gate:%d", "friend:%d", "friend_count:%d",
		"console:v2:attention:{%d}:total", "console:v2:attention:{%d}:participation", "console:v2:attention:{%d}:focus",
	} {
		keys = append(keys, fmt.Sprintf(format, id))
	}
	// Peers' relation and inbox caches are rebuilt from PostgreSQL on the next read.
	for _, peer := range report.Peers {
		for _, format := range []string{"friend:%d", "friend_count:%d", "block:%d", "pm:fetch:%d"} {
			keys = append(keys, fmt.Sprintf(format, peer))
		}
	}
	for _, c := range report.Convs {
		keys = append(keys, fmt.Sprintf("pm:conv:%d", c.ID), fmt.Sprintf("pm:convmap:%d:%d:%d", c.A, c.B, c.Origin))
	}
	if recallNamespace != "" {
		keys = append(keys, fmt.Sprintf("%s:surface:agent:%d:items", recallNamespace, id))
	}
	return keys
}

// RedisHashes are hashes whose field is the agent id (HDEL, never DEL).
var RedisHashes = []string{"agentcard:last_active", "agentcard:influence_percentile", "agentcard:influence_snapshot"}
