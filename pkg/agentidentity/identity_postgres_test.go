package agentidentity_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"eigenflux_server/pkg/agentidentity"
	profiledal "eigenflux_server/rpc/profile/dal"
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

	base := time.Now().UnixNano() / 1000
	agentIDs := []int64{base, base + 1}
	lower, mixed := unusedCasePair(t, tx, agentIDs)
	for index, shortID := range []string{lower, mixed} {
		if err := tx.Exec(`INSERT INTO agents
			(agent_id, short_id, email, agent_name, bio, created_at, updated_at)
			VALUES (?, ?, ?, '', '', ?, ?)`, agentIDs[index], shortID,
			fmt.Sprintf("short-id-contract-%d@example.test", agentIDs[index]), base, base).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Exec(`UPDATE agents SET short_id = short_id WHERE agent_id = ?`, agentIDs[0]).Error; err != nil {
		t.Fatalf("idempotent short-ID write was rejected: %v", err)
	}
	if err := tx.Transaction(func(candidate *gorm.DB) error {
		return candidate.Exec(`UPDATE agents SET short_id = ? WHERE agent_id = ?`, mixed, agentIDs[0]).Error
	}); sqlState(err) != "23514" {
		t.Fatalf("immutable short ID update SQLSTATE=%q err=%v, want 23514", sqlState(err), err)
	}
	if err := tx.Exec(`UPDATE agents SET agent_name = 'renamed' WHERE agent_id = ?`, agentIDs[0]).Error; err != nil {
		t.Fatalf("unrelated Agent update was rejected: %v", err)
	}
	if err := tx.Exec(`UPDATE agents SET short_id = ? WHERE agent_id = ?`, lower, agentIDs[0]).Error; err != nil {
		t.Fatal(err)
	}

	for shortID, wantAgentID := range map[string]int64{lower: agentIDs[0], mixed: agentIDs[1]} {
		got, err := agentidentity.Lookup(context.Background(), tx, shortID)
		if err != nil || got != wantAgentID {
			t.Fatalf("lookup %q=(%d,%v), want %d", shortID, got, err, wantAgentID)
		}
	}

	if err := tx.Transaction(func(candidate *gorm.DB) error {
		return candidate.Exec(`INSERT INTO agents
			(agent_id, short_id, email, agent_name, bio, created_at, updated_at)
			VALUES (?, ?, ?, '', '', ?, ?)`, base+2, lower,
			fmt.Sprintf("short-id-duplicate-%d@example.test", base+2), base, base).Error
	}); sqlState(err) != "23505" {
		t.Fatalf("duplicate exact short ID SQLSTATE=%q err=%v, want 23505", sqlState(err), err)
	}
	if err := tx.Transaction(func(candidate *gorm.DB) error {
		return candidate.Exec(`INSERT INTO agents
			(agent_id, short_id, email, agent_name, bio, created_at, updated_at)
			VALUES (?, 'abc1e', ?, '', '', ?, ?)`, base+3,
			fmt.Sprintf("short-id-illegal-%d@example.test", base+3), base, base).Error
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
			return candidate.Exec(`INSERT INTO agents
				(agent_id, short_id, email, agent_name, bio, created_at, updated_at)
				VALUES (?, NULL, ?, '', '', ?, ?)`, base+4,
				fmt.Sprintf("short-id-missing-%d@example.test", base+4), base, base).Error
		}); err == nil {
			t.Fatal("contract database accepted a missing short ID")
		}
		assertGenericPlanUsesIndex(t, tx, lower, "uq_agents_short_id")
	} else if os.Getenv("AGENT_SHORT_ID_EXPECT_CONTRACT") == "1" {
		t.Fatal("short-ID contract was expected, but agents.short_id is still nullable")
	} else {
		assertGenericPlanUsesIndex(t, tx, lower, "uq_agents_short_id_partial")
	}
}

func TestConcurrentAssignmentConvergesAndRegistrationStaysUnique(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN is required for PostgreSQL integration semantics")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UnixNano() / 1000
	legacyID := base
	if err := db.Exec(`INSERT INTO agents
		(agent_id, short_id, email, agent_name, bio, created_at, updated_at)
		VALUES (?, NULL, ?, '', '', ?, ?)`, legacyID,
		fmt.Sprintf("short-id-concurrent-%d@example.test", legacyID), base, base).Error; err != nil {
		// A contracted database intentionally rejects NULL legacy fixtures. The
		// concurrent registration half below remains applicable there.
		if sqlState(err) != "23502" && sqlState(err) != "23514" {
			t.Fatal(err)
		}
		legacyID = 0
	} else {
		t.Cleanup(func() { _ = db.Exec(`DELETE FROM agents WHERE agent_id = ?`, legacyID).Error })
	}

	if legacyID != 0 {
		candidates := []string{"AaBbC", "DdEeF"}
		var wg sync.WaitGroup
		errs := make(chan error, len(candidates))
		for _, candidate := range candidates {
			candidate := candidate
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs <- db.Exec(`UPDATE agents SET short_id = ?
					WHERE agent_id = ? AND short_id IS NULL`, candidate, legacyID).Error
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		var assigned string
		if err := db.Raw(`SELECT short_id FROM agents WHERE agent_id = ?`, legacyID).Scan(&assigned).Error; err != nil {
			t.Fatal(err)
		}
		if assigned != candidates[0] && assigned != candidates[1] {
			t.Fatalf("concurrent assignment produced %q", assigned)
		}
	}

	const registrations = 32
	registered := make([]int64, registrations)
	errCh := make(chan error, registrations)
	var wg sync.WaitGroup
	for i := 0; i < registrations; i++ {
		i := i
		registered[i] = base + 100 + int64(i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			errCh <- profiledal.CreateAgent(db, &profiledal.Agent{
				AgentID: registered[i],
				Email:   fmt.Sprintf("short-id-registration-%d@example.test", registered[i]),
			})
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _ = db.Exec(`DELETE FROM agents WHERE agent_id IN ?`, registered).Error })
	var uniqueCount int64
	if err := db.Raw(`SELECT COUNT(DISTINCT short_id) FROM agents WHERE agent_id IN ?`, registered).Scan(&uniqueCount).Error; err != nil {
		t.Fatal(err)
	}
	if uniqueCount != registrations {
		t.Fatalf("registered unique short IDs=%d, want %d", uniqueCount, registrations)
	}
}

func unusedCasePair(t *testing.T, tx *gorm.DB, agentIDs []int64) (string, string) {
	t.Helper()
	for attempt := 0; attempt < 100; attempt++ {
		generated, err := agentidentity.GenerateShortID()
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

func assertGenericPlanUsesIndex(t *testing.T, tx *gorm.DB, shortID, indexName string) {
	t.Helper()
	if err := tx.Exec(`SET LOCAL plan_cache_mode = force_generic_plan`).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Exec(`SET LOCAL enable_seqscan = off`).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Exec(`PREPARE agent_short_id_lookup_test(text) AS
		SELECT agent_id FROM agents WHERE short_id = $1 AND short_id IS NOT NULL`).Error; err != nil {
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
	if !strings.Contains(plan.String(), indexName) {
		t.Fatalf("generic lookup plan did not use %s:\n%s", indexName, plan.String())
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
