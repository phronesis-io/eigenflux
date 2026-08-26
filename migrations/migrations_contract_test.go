package migrations_test

import (
	"os"
	"strings"
	"testing"
)

func migration(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestShortIDMigrationNeverDropsLiveTriggerDuringUp(t *testing.T) {
	sql := migration(t, "000075_agent_short_id_expand.sql")
	up, _, ok := strings.Cut(sql, "-- +goose Down")
	if !ok {
		t.Fatal("migration has no Down boundary")
	}
	if strings.Contains(up, "DROP TRIGGER") {
		t.Fatal("short-ID Up must not remove immutability protection during a retry")
	}
	for _, required := range []string{"pg_trigger", "NOT tgisinternal", "CREATE TRIGGER trg_agents_short_id_immutable"} {
		if !strings.Contains(up, required) {
			t.Fatalf("short-ID retry-safe trigger guard missing %q", required)
		}
	}
}

func TestShortIDMigrationDownFailsClosed(t *testing.T) {
	sql := migration(t, "000075_agent_short_id_expand.sql")
	_, down, ok := strings.Cut(sql, "-- +goose Down")
	if !ok || !strings.Contains(down, "short IDs and invite revocation history are permanent") {
		t.Fatal("short-ID Down must fail closed before destructive DDL")
	}
}

func TestNetworkMemberRepairLocksBeforeInspectionAndRebuildsSet(t *testing.T) {
	sql := migration(t, "000077_repair_agent_network_member_numbers.sql")
	lock := strings.Index(sql, "LOCK TABLE agent_network_memberships IN ACCESS EXCLUSIVE MODE")
	inspection := strings.Index(sql, "IF EXISTS")
	if lock < 0 || inspection < 0 || lock > inspection {
		t.Fatal("member repair must lock before inspecting the membership set")
	}
	for _, required := range []string{"DELETE FROM agent_network_memberships", "INSERT INTO agent_network_memberships", "PERFORM setval"} {
		if !strings.Contains(sql, required) {
			t.Fatalf("member repair does not reconcile the full set; missing %q", required)
		}
	}
}
