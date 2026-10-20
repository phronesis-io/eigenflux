package testutil

import (
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestEnsureLegacyTestAgentPostgres(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN is required for the local PostgreSQL fixture contract")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid PostgreSQL fixture configuration")
	}
	if cfg.Host != "localhost" && cfg.Host != "127.0.0.1" && cfg.Host != "::1" {
		t.Fatal("fixture contract requires loopback PostgreSQL")
	}
	admin := stdlib.OpenDB(*cfg)
	defer admin.Close()
	schema := "legacy_fixture_" + uuid.New().String()[:8]
	name := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec("CREATE SCHEMA " + name); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec("DROP SCHEMA " + name + " CASCADE")
	for _, table := range []string{"agents", "agent_profiles"} {
		if _, err := admin.Exec("CREATE TABLE " + name + "." + pgx.Identifier{table}.Sanitize() + " (LIKE public." + pgx.Identifier{table}.Sanitize() + " INCLUDING ALL)"); err != nil {
			t.Fatal(err)
		}
	}
	cfg.RuntimeParams["search_path"] = schema
	fixture := stdlib.OpenDB(*cfg)
	defer fixture.Close()
	previous := TestDB
	TestDB = fixture
	defer func() { TestDB = previous }()
	EnsureLegacyTestAgent(t, "  LEGACY_FIXTURE@TEST.COM ")
	var id int64
	var shortID, kind string
	if err := fixture.QueryRow("SELECT agent_id,short_id,email_kind FROM agents WHERE email='legacy_fixture@test.com'").Scan(&id, &shortID, &kind); err != nil {
		t.Fatal(err)
	}
	if id <= 0 || len(shortID) != 5 || kind != "legacy_real" {
		t.Fatalf("invalid legacy fixture: %d %q %q", id, shortID, kind)
	}
	if _, err := fixture.Exec("UPDATE agents SET agent_name='owner profile' WHERE agent_id=$1", id); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.Exec("UPDATE agent_profiles SET status=3 WHERE agent_id=$1", id); err != nil {
		t.Fatal(err)
	}
	EnsureLegacyTestAgent(t, "legacy_fixture@test.com")
	var savedID int64
	var savedShortID, ownerName string
	var status int
	if err := fixture.QueryRow("SELECT a.agent_id,a.short_id,a.agent_name,p.status FROM agents a JOIN agent_profiles p USING(agent_id)").Scan(&savedID, &savedShortID, &ownerName, &status); err != nil {
		t.Fatal(err)
	}
	if savedID != id || savedShortID != shortID || ownerName != "owner profile" || status != 3 {
		t.Fatal("fixture seeding overwrote existing identity/profile")
	}
}
