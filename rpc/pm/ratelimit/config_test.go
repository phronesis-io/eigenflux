package ratelimit

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFileAndHourlyLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "limits.yaml")
	if err := os.WriteFile(path, []byte("default_hourly_limit: 10\noverrides:\n  - agent_id: 101\n    hourly_limit: 200\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.HourlyLimit(101); got != 200 {
		t.Fatalf("HourlyLimit(101) = %d, want 200", got)
	}
	if got := cfg.HourlyLimit(202); got != 10 {
		t.Fatalf("HourlyLimit(202) = %d, want 10", got)
	}
}

func TestValidateRejectsDuplicateOverrides(t *testing.T) {
	cfg := &Config{DefaultHourlyLimit: 10, Overrides: []Override{{AgentID: 101, HourlyLimit: 200}, {AgentID: 101, HourlyLimit: 100}}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected duplicate override to be rejected")
	}
}
