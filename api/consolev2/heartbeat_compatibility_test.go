package consolev2

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestLoadConsoleV2CompatibilityQueryTypesAgentIDAsBigInt(t *testing.T) {
	if !strings.Contains(loadConsoleV2CompatibilityQuery, "CAST(? AS BIGINT)") {
		t.Fatalf("compatibility query must cast the bound Agent ID to BIGINT: %s", loadConsoleV2CompatibilityQuery)
	}
	if strings.Contains(loadConsoleV2CompatibilityQuery, "SELECT ? AS agent_id") {
		t.Fatal("compatibility query must not leave the Agent ID bind parameter untyped")
	}
}

func TestConsoleV2CompatibilityGate(t *testing.T) {
	tests := []struct {
		name, cli, contract, revision, status, reason string
		onboardingCompleted                           bool
		available                                     bool
	}{
		{name: "missing report", status: "unknown", reason: "report_missing"},
		{name: "old cli", cli: "0.0.33", contract: heartbeatContractV1, revision: "r1", status: "upgrade_required", reason: "cli_outdated"},
		{name: "old heartbeat is accepted", cli: "0.0.35", contract: "legacy", revision: "r1", status: "ready", available: true},
		{name: "missing skills is accepted", cli: "0.0.35", contract: heartbeatContractV1, status: "ready", available: true},
		{name: "minimum ready", cli: "0.0.35", contract: heartbeatContractV1, revision: "r1", status: "ready", available: true},
		{name: "completed onboarding bypasses missing report", onboardingCompleted: true, status: "ready", available: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := consoleV2Compatibility(test.cli, test.contract, test.revision, 123, test.onboardingCompleted)
			if got["available"] != test.available || got["status"] != test.status || got["reason"] != test.reason {
				t.Fatalf("compatibility = %#v", got)
			}
			if got["minimum_cli_version"] != minimumConsoleV2CLI || got["required_heartbeat_contract_version"] != heartbeatContractV1 {
				t.Fatalf("gate requirements = %#v", got)
			}
		})
	}
}

func TestCompareConsoleCLIVersion(t *testing.T) {
	for _, test := range []struct {
		left, right string
		want        int
	}{
		{"0.0.34", "0.0.35", -1}, {"v0.0.35", "0.0.35", 0}, {"0.0.36-dev.1", "0.0.35", 1},
	} {
		if got := compareConsoleCLIVersion(test.left, test.right); got != test.want {
			t.Fatalf("compare(%q,%q)=%d want %d", test.left, test.right, got, test.want)
		}
	}
}

func TestRecoveryRefreshRequiresCLI0035(t *testing.T) {
	for _, test := range []struct {
		name            string
		refreshRequired bool
		cliVersion      string
		want            bool
	}{
		{name: "ordinary refresh remains compatible", cliVersion: "0.0.34", want: true},
		{name: "recovery blocks 0.0.34", refreshRequired: true, cliVersion: "0.0.34"},
		{name: "recovery blocks missing version", refreshRequired: true},
		{name: "recovery allows 0.0.35", refreshRequired: true, cliVersion: "0.0.35", want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := recoveryRefreshCLIAllowed(test.refreshRequired, test.cliVersion); got != test.want {
				t.Fatalf("allowed = %t, want %t", got, test.want)
			}
		})
	}
}

func TestLegacyConsoleCompatibilityHandlerBypassesCompletedOnboarding(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE agent_settings (
			agent_id INTEGER PRIMARY KEY, cli_version TEXT,
			heartbeat_contract_version TEXT, skill_revision TEXT,
			heartbeat_reported_at INTEGER
		)`,
		`CREATE TABLE agent_onboarding_v2 (agent_id INTEGER PRIMARY KEY, state TEXT)`,
		`INSERT INTO agent_settings
			(agent_id, cli_version, heartbeat_contract_version, skill_revision, heartbeat_reported_at)
			VALUES (42, '', '', '', 0)`,
		`INSERT INTO agent_onboarding_v2 (agent_id, state) VALUES (42, 'completed')`,
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}

	service := &Service{db: db}
	request := app.NewContext(0)
	request.Set("agent_id", int64(42))
	service.LegacyConsoleCompatibilityHandler()(context.Background(), request)

	if request.Response.StatusCode() != http.StatusOK {
		t.Fatalf("status = %d, body = %s", request.Response.StatusCode(), request.Response.Body())
	}
	var envelope struct {
		Code int `json:"code"`
		Data struct {
			Compatibility map[string]interface{} `json:"compatibility"`
			Onboarding    struct {
				State string `json:"state"`
			} `json:"onboarding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(request.Response.Body(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Code != 0 || envelope.Data.Onboarding.State != "completed" || envelope.Data.Compatibility["available"] != true {
		t.Fatalf("unexpected compatibility envelope: %#v", envelope)
	}
}

func TestLegacyConsoleCompatibilityHandlerStillGatesIncompleteOnboarding(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE agent_settings (
			agent_id INTEGER PRIMARY KEY, cli_version TEXT,
			heartbeat_contract_version TEXT, skill_revision TEXT,
			heartbeat_reported_at INTEGER
		)`,
		`CREATE TABLE agent_onboarding_v2 (agent_id INTEGER PRIMARY KEY, state TEXT)`,
		`INSERT INTO agent_onboarding_v2 (agent_id, state) VALUES (43, 'in_progress')`,
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}

	compatibility, onboardingState, err := (&Service{db: db}).loadConsoleV2Compatibility(43)
	if err != nil {
		t.Fatal(err)
	}
	if onboardingState != "in_progress" || compatibility["available"] != false || compatibility["reason"] != "report_missing" {
		t.Fatalf("unexpected incomplete compatibility: state=%q compatibility=%#v", onboardingState, compatibility)
	}
}
