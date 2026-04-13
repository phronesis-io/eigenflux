package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCreatesDefault(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("EIGENFLUX_HOME", dir)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.DefaultServer != "eigenflux" {
		t.Errorf("DefaultServer = %q, want %q", cfg.DefaultServer, "eigenflux")
	}
	i := cfg.findServer("eigenflux")
	if i < 0 {
		t.Fatal("expected eigenflux server to exist")
	}
	if cfg.Servers[i].Endpoint != "https://www.eigenflux.ai" {
		t.Errorf("default endpoint = %q, want %q", cfg.Servers[i].Endpoint, "https://www.eigenflux.ai")
	}
	if cfg.Servers[i].StreamEndpoint != "wss://stream.eigenflux.ai" {
		t.Errorf("default stream endpoint = %q, want %q", cfg.Servers[i].StreamEndpoint, "wss://stream.eigenflux.ai")
	}
}

func TestAddAndRemoveServer(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("EIGENFLUX_HOME", dir)
	cfg, _ := Load()
	err := cfg.AddServer("staging", "https://staging.eigenflux.ai")
	if err != nil {
		t.Fatalf("AddServer error: %v", err)
	}
	if cfg.findServer("staging") < 0 {
		t.Error("expected staging server")
	}
	err = cfg.AddServer("staging", "https://other.eigenflux.ai")
	if err == nil {
		t.Error("expected error for duplicate server name")
	}
	err = cfg.RemoveServer("staging")
	if err != nil {
		t.Fatalf("RemoveServer error: %v", err)
	}
	if cfg.findServer("staging") >= 0 {
		t.Error("staging should be removed")
	}
	err = cfg.RemoveServer("eigenflux")
	if err == nil {
		t.Error("expected error removing default server")
	}
}

func TestSetCurrent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("EIGENFLUX_HOME", dir)
	cfg, _ := Load()
	cfg.AddServer("staging", "https://staging.eigenflux.ai")
	err := cfg.SetCurrent("staging")
	if err != nil {
		t.Fatalf("SetCurrent error: %v", err)
	}
	if cfg.DefaultServer != "staging" {
		t.Errorf("DefaultServer = %q, want %q", cfg.DefaultServer, "staging")
	}
	err = cfg.SetCurrent("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent server")
	}
}

func TestGetActive(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("EIGENFLUX_HOME", dir)
	cfg, _ := Load()
	cfg.AddServer("staging", "https://staging.eigenflux.ai")
	srv, err := cfg.GetActive("")
	if err != nil {
		t.Fatalf("GetActive error: %v", err)
	}
	if srv.Name != "eigenflux" {
		t.Errorf("active = %q, want %q", srv.Name, "eigenflux")
	}
	srv, err = cfg.GetActive("staging")
	if err != nil {
		t.Fatalf("GetActive(staging) error: %v", err)
	}
	if srv.Name != "staging" {
		t.Errorf("active = %q, want %q", srv.Name, "staging")
	}
}

func TestUpdateServer(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("EIGENFLUX_HOME", dir)
	cfg, _ := Load()
	err := cfg.UpdateServer("eigenflux", "https://new.eigenflux.ai", "")
	if err != nil {
		t.Fatalf("UpdateServer error: %v", err)
	}
	i := cfg.findServer("eigenflux")
	if cfg.Servers[i].Endpoint != "https://new.eigenflux.ai" {
		t.Errorf("endpoint = %q, want %q", cfg.Servers[i].Endpoint, "https://new.eigenflux.ai")
	}
}

func TestSaveAndReload(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("EIGENFLUX_HOME", dir)
	cfg, _ := Load()
	cfg.AddServer("staging", "https://staging.eigenflux.ai")
	cfg.Save()
	cfg2, err := Load()
	if err != nil {
		t.Fatalf("reload error: %v", err)
	}
	if cfg2.findServer("staging") < 0 {
		t.Error("staging server should persist after save/reload")
	}
}

func TestHomeDir(t *testing.T) {
	// Env var without .eigenflux suffix — should auto-append.
	dir := t.TempDir()
	t.Setenv("EIGENFLUX_HOME", dir)
	home := HomeDir()
	want := filepath.Join(dir, ".eigenflux")
	if home != want {
		t.Errorf("HomeDir = %q, want %q", home, want)
	}

	// Env var already ending in .eigenflux — no double suffix.
	efDir := filepath.Join(t.TempDir(), ".eigenflux")
	t.Setenv("EIGENFLUX_HOME", efDir)
	home = HomeDir()
	if home != efDir {
		t.Errorf("HomeDir = %q, want %q (should not double-suffix)", home, efDir)
	}

	// No env var — default to ~/.eigenflux.
	t.Setenv("EIGENFLUX_HOME", "")
	os.Unsetenv("EIGENFLUX_HOME")
	home = HomeDir()
	expected := filepath.Join(os.Getenv("HOME"), ".eigenflux")
	if home != expected {
		t.Errorf("HomeDir = %q, want %q", home, expected)
	}
}

func TestSetHomeDir_OverridesEnv(t *testing.T) {
	envDir := t.TempDir()
	flagDir := t.TempDir()

	t.Setenv("EIGENFLUX_HOME", envDir)
	SetHomeDir(flagDir)
	t.Cleanup(func() { SetHomeDir("") })

	got := HomeDir()
	want := filepath.Join(flagDir, ".eigenflux")
	if got != want {
		t.Errorf("HomeDir = %q, want %q (--homedir should override env)", got, want)
	}
}

func TestSetHomeDir_Empty_FallsBackToEnv(t *testing.T) {
	envDir := t.TempDir()
	t.Setenv("EIGENFLUX_HOME", envDir)
	SetHomeDir("")

	got := HomeDir()
	want := filepath.Join(envDir, ".eigenflux")
	if got != want {
		t.Errorf("HomeDir = %q, want %q (empty override should fall back to env)", got, want)
	}

}

func TestSetHomeDir_AlreadySuffixed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".eigenflux")
	SetHomeDir(dir)
	t.Cleanup(func() { SetHomeDir("") })

	got := HomeDir()
	if got != dir {
		t.Errorf("HomeDir = %q, want %q (should not double-suffix)", got, dir)
	}
}
