package agentidentity

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestPostgresShortIDCaseAndContractSemantics(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN is required for PostgreSQL integration semantics")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })

	var agentIDs []int64
	if err := tx.Raw(`SELECT agent_id FROM agents ORDER BY agent_id LIMIT 2 FOR UPDATE`).Scan(&agentIDs).Error; err != nil {
		t.Fatal(err)
	}
	if len(agentIDs) < 2 {
		t.Skip("requires two Agents in the PostgreSQL integration database")
	}
	lower, mixed := unusedCasePair(t, tx, agentIDs)
	if err := tx.Exec(`UPDATE agents SET short_id = ? WHERE agent_id = ?`, lower, agentIDs[0]).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Exec(`UPDATE agents SET short_id = ? WHERE agent_id = ?`, mixed, agentIDs[1]).Error; err != nil {
		t.Fatalf("case-distinct short IDs must coexist: %v", err)
	}

	for shortID, wantAgentID := range map[string]int64{lower: agentIDs[0], mixed: agentIDs[1]} {
		got, err := Lookup(context.Background(), tx, shortID)
		if err != nil || got != wantAgentID {
			t.Fatalf("lookup %q=(%d,%v), want %d", shortID, got, err, wantAgentID)
		}
	}

	if err := tx.Transaction(func(candidate *gorm.DB) error {
		return candidate.Exec(`UPDATE agents SET short_id = ? WHERE agent_id = ?`, lower, agentIDs[1]).Error
	}); sqlState(err) != "23505" {
		t.Fatalf("duplicate exact short ID SQLSTATE=%q err=%v, want 23505", sqlState(err), err)
	}
	if err := tx.Transaction(func(candidate *gorm.DB) error {
		return candidate.Exec(`UPDATE agents SET short_id = 'abc1e' WHERE agent_id = ?`, agentIDs[1]).Error
	}); err == nil {
		t.Fatal("illegal short ID was accepted")
	}

	var nullable string
	if err := tx.Raw(`SELECT is_nullable FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'agents' AND column_name = 'short_id'`).Scan(&nullable).Error; err != nil {
		t.Fatal(err)
	}
	if nullable == "NO" {
		if err := tx.Transaction(func(candidate *gorm.DB) error {
			return candidate.Exec(`UPDATE agents SET short_id = NULL WHERE agent_id = ?`, agentIDs[1]).Error
		}); err == nil {
			t.Fatal("contract database accepted a missing short ID")
		}
		assertGenericPlanUsesCompleteIndex(t, tx, lower)
	} else if os.Getenv("AGENT_SHORT_ID_EXPECT_CONTRACT") == "1" {
		t.Fatal("short-ID contract was expected, but agents.short_id is still nullable")
	}
}

func unusedCasePair(t *testing.T, tx *gorm.DB, agentIDs []int64) (string, string) {
	t.Helper()
	for attempt := 0; attempt < 100; attempt++ {
		generated, err := GenerateShortID()
		if err != nil {
			t.Fatal(err)
		}
		lower := strings.ToLower(generated)
		mixed := strings.ToUpper(lower[:1]) + lower[1:]
		upper := strings.ToUpper(lower)
		var count int64
		if err := tx.Raw(`SELECT COUNT(*) FROM agents
			WHERE agent_id NOT IN (?, ?) AND short_id IN (?, ?, ?)`,
			agentIDs[0], agentIDs[1], lower, mixed, upper).Scan(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count == 0 {
			return lower, mixed
		}
	}
	t.Fatal("could not find an unused case-sensitive short-ID pair")
	return "", ""
}

func assertGenericPlanUsesCompleteIndex(t *testing.T, tx *gorm.DB, shortID string) {
	t.Helper()
	if err := tx.Exec(`SET LOCAL plan_cache_mode = force_generic_plan`).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Exec(`SET LOCAL enable_seqscan = off`).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Exec(`PREPARE agent_short_id_lookup_test(text) AS
		SELECT agent_id FROM agents WHERE short_id = $1`).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Exec(`DEALLOCATE agent_short_id_lookup_test`).Error })
	var planRows []struct {
		Plan string `gorm:"column:QUERY PLAN"`
	}
	query := fmt.Sprintf(`EXPLAIN (FORMAT TEXT) EXECUTE agent_short_id_lookup_test('%s')`, shortID)
	if err := tx.Raw(query).Scan(&planRows).Error; err != nil {
		t.Fatal(err)
	}
	var plan strings.Builder
	for _, row := range planRows {
		plan.WriteString(row.Plan)
		plan.WriteByte('\n')
	}
	if !strings.Contains(plan.String(), "uq_agents_short_id") {
		t.Fatalf("generic lookup plan did not use complete short-ID index:\n%s", plan.String())
	}
}

func sqlState(err error) string {
	if err == nil {
		return ""
	}
	var state interface{ SQLState() string }
	if ok := errors.As(err, &state); ok {
		return state.SQLState()
	}
	return ""
}
