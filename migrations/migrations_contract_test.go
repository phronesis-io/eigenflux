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
	sql := migration(t, "000076_agent_short_id_expand.sql")
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
	sql := migration(t, "000076_agent_short_id_expand.sql")
	_, down, ok := strings.Cut(sql, "-- +goose Down")
	if !ok || !strings.Contains(down, "short IDs and invite revocation history are permanent") {
		t.Fatal("short-ID Down must fail closed before destructive DDL")
	}
}

func TestShortIDBackfillUsesOneSetBasedStatementPerBatch(t *testing.T) {
	raw, err := os.ReadFile("../scripts/common/agent_short_id_backfill.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, forbidden := range []string{"SAVEPOINT", "assignShortID(db, agentID)"} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("short-ID backfill must not use per-Agent subtransactions; found %q", forbidden)
		}
	}
	for _, required := range []string{"shortIDBackfillBatch = 100", "jsonb_to_recordset", "context.WithTimeout"} {
		if !strings.Contains(source, required) {
			t.Fatalf("short-ID set-based backfill contract missing %q", required)
		}
	}
}

func TestNetworkMemberRepairLocksBeforeInspectionAndRebuildsSet(t *testing.T) {
	sql := migration(t, "000078_repair_agent_network_member_numbers.sql")
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

func TestAttentionExpandRejectsLegacyWithoutTriggerGap(t *testing.T) {
	sql := migration(t, "000077_console_v2_agent_attention_protocol.sql")
	up, _, ok := strings.Cut(sql, "-- +goose Down")
	if !ok {
		t.Fatal("Attention migration has no Down boundary")
	}
	if strings.Contains(up, "DROP TRIGGER") {
		t.Fatal("Attention expand must not remove legacy-write protection during a retry")
	}
	for _, required := range []string{
		"protocol_version VARCHAR(32)",
		"NEW.protocol_version IS DISTINCT FROM 'agent_attention.v1'",
		"pg_trigger",
		"CREATE TRIGGER trg_reject_legacy_attention_write",
	} {
		if !strings.Contains(up, required) {
			t.Fatalf("Attention expand contract missing %q", required)
		}
	}
}

func TestAttentionContractPersistsItemAndCommandProtocol(t *testing.T) {
	sql := migration(t, "000080_console_v2_agent_attention_contract.sql")
	for _, required := range []string{
		"protocol_version = 'agent_attention.v1'",
		"payload->>'protocol_version' = 'agent_attention.v1'",
		"chk_agent_commands_attention_protocol",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("Attention protocol contract missing %q", required)
		}
	}
}
