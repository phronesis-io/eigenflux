package consolev2

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestConsoleV2CompatibilityGate(t *testing.T) {
	tests := []struct {
		name, cli, contract, revision, status, reason string
		available                                     bool
	}{
		{name: "missing report", status: "unknown", reason: "report_missing"},
		{name: "old cli", cli: "0.0.33", contract: heartbeatContractV1, revision: "r1", status: "upgrade_required", reason: "cli_outdated"},
		{name: "old heartbeat", cli: "0.0.34", contract: "legacy", revision: "r1", status: "upgrade_required", reason: "heartbeat_outdated"},
		{name: "missing skills", cli: "0.0.34", contract: heartbeatContractV1, status: "upgrade_required", reason: "skills_unknown"},
		{name: "minimum ready", cli: "0.0.34", contract: heartbeatContractV1, revision: "r1", status: "ready", available: true},
		{name: "newer ready", cli: "1.2.3", contract: heartbeatContractV1, revision: "r2", status: "ready", available: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := consoleV2Compatibility(test.cli, test.contract, test.revision, 123)
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
		{"0.0.33", "0.0.34", -1}, {"v0.0.34", "0.0.34", 0}, {"0.0.35-dev.1", "0.0.34", 1},
	} {
		if got := compareConsoleCLIVersion(test.left, test.right); got != test.want {
			t.Fatalf("compare(%q,%q)=%d want %d", test.left, test.right, got, test.want)
		}
	}
}

func TestLegacyConsoleCompatibilityHandlerUsesV1Envelope(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE agent_settings (
		agent_id INTEGER PRIMARY KEY,
		cli_version TEXT,
		heartbeat_contract_version TEXT,
		skill_revision TEXT,
		heartbeat_reported_at INTEGER
	)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO agent_settings
		(agent_id, cli_version, heartbeat_contract_version, skill_revision, heartbeat_reported_at)
		VALUES (?, ?, ?, ?, ?)`, 42, "0.0.34-test", heartbeatContractV1, "skills-r1", 123).Error; err != nil {
		t.Fatal(err)
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
		} `json:"data"`
	}
	if err := json.Unmarshal(request.Response.Body(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Code != 0 || envelope.Data.Compatibility["available"] != true {
		t.Fatalf("unexpected compatibility envelope: %#v", envelope)
	}
	if envelope.Data.Compatibility["cli_version"] != "0.0.34-test" {
		t.Fatalf("CLI version was not preserved: %#v", envelope.Data.Compatibility)
	}
}
