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

func TestResolveConfigPath(t *testing.T) {
	t.Run("explicit override wins", func(t *testing.T) {
		got := resolveConfigPath(" /custom/limits.yaml ", true, "/stable/limits.yaml", "legacy.yaml", func(string) error {
			t.Fatal("stat should not run for an explicit path")
			return nil
		})
		if got != "/custom/limits.yaml" {
			t.Fatalf("resolveConfigPath() = %q, want explicit path", got)
		}
	})

	t.Run("production always uses stable path", func(t *testing.T) {
		got := resolveConfigPath("", true, "/stable/limits.yaml", "legacy.yaml", func(string) error {
			t.Fatal("stat should not run in production")
			return nil
		})
		if got != "/stable/limits.yaml" {
			t.Fatalf("resolveConfigPath() = %q, want stable path", got)
		}
	})

	t.Run("stable production path exists", func(t *testing.T) {
		got := resolveConfigPath("", false, "/stable/limits.yaml", "legacy.yaml", func(path string) error {
			if path != "/stable/limits.yaml" {
				t.Fatalf("stat path = %q", path)
			}
			return nil
		})
		if got != "/stable/limits.yaml" {
			t.Fatalf("resolveConfigPath() = %q, want stable path", got)
		}
	})

	t.Run("stable path errors do not silently fall back", func(t *testing.T) {
		got := resolveConfigPath("", false, "/stable/limits.yaml", "legacy.yaml", func(string) error {
			return os.ErrPermission
		})
		if got != "/stable/limits.yaml" {
			t.Fatalf("resolveConfigPath() = %q, want stable path", got)
		}
	})

	t.Run("missing stable path uses local legacy path", func(t *testing.T) {
		got := resolveConfigPath("", false, "/stable/limits.yaml", "legacy.yaml", func(string) error {
			return os.ErrNotExist
		})
		if got != "legacy.yaml" {
			t.Fatalf("resolveConfigPath() = %q, want legacy path", got)
		}
	})
}
