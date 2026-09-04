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

func TestCLIAccountSwitchNoopMigrationDownPreservesAuditHistory(t *testing.T) {
	sql := migration(t, "000096_console_v2_cli_account_switch_noop.sql")
	up, down, ok := strings.Cut(sql, "-- +goose Down")
	if !ok {
		t.Fatal("CLI account-switch no-op migration has no Down boundary")
	}
	guard := "cannot downgrade while completed_noop account-switch history exists"
	if strings.Contains(up, guard) || !strings.Contains(down, guard) {
		t.Fatal("completed_noop downgrade guard must fail closed in Down only")
	}
	for _, destructiveRewrite := range []string{"SET result = 'revoked'", "SET status = 'revoked'"} {
		if strings.Contains(down, destructiveRewrite) {
			t.Fatalf("Down must not rewrite completed_noop audit history with %q", destructiveRewrite)
		}
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

func TestHeartbeatCompatibilityMigrationIsAdditive(t *testing.T) {
	sql := migration(t, "000085_console_v2_heartbeat_compatibility.sql")
	up, _, ok := strings.Cut(sql, "-- +goose Down")
	if !ok {
		t.Fatal("heartbeat compatibility migration has no Down boundary")
	}
	for _, required := range []string{
		"ADD COLUMN IF NOT EXISTS heartbeat_contract_version",
		"ADD COLUMN IF NOT EXISTS skill_revision",
		"ADD COLUMN IF NOT EXISTS heartbeat_reported_at",
	} {
		if !strings.Contains(up, required) {
			t.Fatalf("heartbeat compatibility migration missing %q", required)
		}
	}
	if strings.Contains(up, "DROP ") || strings.Contains(up, "UPDATE ") {
		t.Fatal("heartbeat compatibility Up must remain additive and avoid table rewrites")
	}
}

func TestTodayModelBriefStorageIsBoundedPerAgentLanguage(t *testing.T) {
	sql := migration(t, "000086_console_v2_today_model_briefs.sql")
	for _, required := range []string{
		"PRIMARY KEY (agent_id, language)",
		"language IN ('zh-CN', 'en')",
		"char_length(narrative) <= 280",
		"ON DELETE CASCADE",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("Today model brief storage contract missing %q", required)
		}
	}
	if strings.Contains(sql, "CREATE INDEX") {
		t.Fatal("bounded primary-key lookups do not need an additional index")
	}
}

func TestConsoleV2ConnectionAuthUnificationMigration(t *testing.T) {
	sql := migration(t, "000087_console_v2_connection_auth_unification.sql")
	for _, required := range []string{
		"language = 'zh-CN' AND char_length(narrative) <= 60",
		"language = 'en' AND char_length(narrative) <= 120",
		"'feed:feedback'", "'relations:read'", "'relations:write'",
		"'profile:read'", "'settings:read'", "'settings:write'",
		"onboarding.state = 'completed'",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("connection/auth migration missing %q", required)
		}
	}
}

func TestCompletedAgentV2SessionScopeRepairMigration(t *testing.T) {
	sql := migration(t, "000088_repair_completed_agent_v2_session_scopes.sql")
	for _, required := range []string{
		"session.audience = 'agent_v2'", "session.revoked_at IS NULL",
		"session.expires_at >", "onboarding.state = 'completed'",
		"principal.status = 'active'", "'feed:feedback'", "'communication:read'",
		"'profile:read'", "'settings:write'", "'attention:write'",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("completed Agent V2 scope repair missing %q", required)
		}
	}
}

func TestHistoricalAgentRecoveryMigrationPreservesIdentityHistory(t *testing.T) {
	sql := migration(t, "000090_console_v2_account_recovery.sql")
	for _, required := range []string{
		"identity_state IN ('active', 'recovered_temporary')",
		"WHERE identity_state = 'active'",
		"client_capabilities TEXT[]",
		"revoked_at BIGINT",
		"WHERE consumed_at IS NULL AND revoked_at IS NULL",
		"access_refresh_required BOOLEAN",
		"source_agent_id <> target_agent_id",
		"source_disposition IN ('abandon', 'preserve')",
		"WHERE status = 'completed'",
		"idx_agent_account_recovery_source_completed",
		"agent_account_recovery_audit",
		"account recovery history is permanent",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("historical Agent recovery migration missing %q", required)
		}
	}
	if strings.Contains(sql, "otp_hmac") || strings.Contains(sql, "public_key") {
		t.Fatal("account recovery audit schema must not persist OTP or key material")
	}
	if strings.Contains(sql, "CREATE UNIQUE INDEX uq_agent_account_recovery_source_terminal") {
		t.Fatal("formal accounts must be able to switch away and later switch back")
	}
}

func TestAttentionPrefillMigrationAddsExplicitPhaseAndRepairsOnlyIncompleteSessions(t *testing.T) {
	sql := migration(t, "000091_console_v2_attention_prefill.sql")
	up, _, ok := strings.Cut(sql, "-- +goose Down")
	if !ok {
		t.Fatal("Attention Prefill migration has no Down boundary")
	}
	for _, required := range []string{
		"attention_phase VARCHAR(16)",
		"attention_phase IN ('prefill', 'active')",
		"chk_agent_attention_phase",
	} {
		if !strings.Contains(up, required) {
			t.Fatalf("Attention Prefill migration missing %q", required)
		}
	}
	for _, required := range []string{
		"UPDATE agent_credential_sessions",
		"attention:prefill",
		"onboarding.state <> 'completed'",
		"session.revoked_at IS NULL",
		"session.expires_at >",
	} {
		if !strings.Contains(up, required) {
			t.Fatalf("Attention Prefill session repair missing %q", required)
		}
	}
	if strings.Contains(up, "onboarding.state = 'completed'") {
		t.Fatal("Attention Prefill session repair must not grant setup scope to completed Agents")
	}
}

func TestLegacyUpgradeGrantMigrationBindsProvisionToExistingAgent(t *testing.T) {
	sql := migration(t, "000092_console_v2_legacy_agent_upgrade_grants.sql")
	for _, required := range []string{
		"subject_agent_id BIGINT",
		"REFERENCES agents(agent_id)",
		"idx_agent_bootstrap_grants_subject",
		"subject-bound grants preserve existing identities",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("legacy Agent upgrade grant migration missing %q", required)
		}
	}
}

func TestCLIAccountSwitchMigrationKeepsPendingSwitchServerSide(t *testing.T) {
	sql := migration(t, "000093_console_v2_cli_account_switch.sql")
	for _, required := range []string{
		"agent_cli_account_switches", "source_agent_id", "target_agent_id",
		"source_console_session_id", "target_console_session_id",
		"pending_target", "pending_onboarding", "ownership_verified_at",
		"idx_agent_cli_account_switches_principal_pending",
		"agent_cli_account_switch_audit",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("CLI account switch migration missing %q", required)
		}
	}
	if strings.Contains(sql, "otp_hmac") || strings.Contains(sql, "session_secret_hash") {
		t.Fatal("CLI account switch state must not persist OTP or Console session secrets")
	}
}

func TestCLIAccountSwitchNoopMigrationAddsTerminalState(t *testing.T) {
	sql := migration(t, "000096_console_v2_cli_account_switch_noop.sql")
	up, down, ok := strings.Cut(sql, "-- +goose Down")
	if !ok {
		t.Fatal("CLI account-switch no-op migration has no Down boundary")
	}
	for _, required := range []string{
		"completed_noop", "target_agent_id IS NULL", "completed_at IS NOT NULL",
		"chk_agent_cli_account_switch_audit_result",
	} {
		if !strings.Contains(up, required) {
			t.Fatalf("CLI account-switch no-op migration missing %q", required)
		}
	}
	if !strings.Contains(down, "cannot downgrade while completed_noop account-switch history exists") {
		t.Fatal("CLI account-switch no-op migration must fail closed instead of rewriting terminal history")
	}
}

func TestAgentCapabilityScopeMigrationRepairsOnlyActiveCompletedSessions(t *testing.T) {
	sql := migration(t, "000094_console_v2_agent_context_write.sql")
	for _, required := range []string{
		"UPDATE agent_credential_sessions", "context:write", "attention:read", "onboarding.state = 'completed'",
		"session.revoked_at IS NULL", "session.expires_at >", "session.scopes || ARRAY['context:write', 'attention:read']",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("Agent context-write migration missing %q", required)
		}
	}
	if strings.Contains(sql, "onboarding.state <> 'completed'") {
		t.Fatal("context:write must not be granted to incomplete Agents")
	}
}
func TestHomepageEligibilityMigrationIsVersionedAndIndexed(t *testing.T) {
	sql := migration(t, "000095_homepage_content_eligibility.sql")
	for _, required := range []string{
		"homepage_eligible BOOLEAN",
		"homepage_rejection_reason VARCHAR(32)",
		"homepage_evaluation_version VARCHAR(32)",
		"homepage_evaluated_at BIGINT",
		"homepage_evaluation_retry_at BIGINT",
		"WHERE status = 3 AND homepage_eligible = TRUE",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("homepage eligibility migration missing %q", required)
		}
	}
}

func TestHomepageRealWorldRelevanceMigrationIsIndexed(t *testing.T) {
	sql := migration(t, "000096_homepage_real_world_relevance.sql")
	for _, required := range []string{
		"homepage_real_world_relevant BOOLEAN NOT NULL DEFAULT FALSE",
		"homepage_eligible = TRUE AND homepage_real_world_relevant = TRUE",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("homepage real-world relevance migration missing %q", required)
		}
	}
}
