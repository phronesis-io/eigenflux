package client

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveMeta(t *testing.T) {
	m := ResolveMeta()
	if m.OS == "" {
		t.Error("OS should not be empty")
	}
	if m.TZ == "" {
		t.Error("TZ should not be empty")
	}
	if m.Host != "terminal" {
		t.Errorf("Host = %q, want %q (default)", m.Host, "terminal")
	}
	if m.Channel != "cli" {
		t.Errorf("Channel = %q, want %q (default)", m.Channel, "cli")
	}
}

func TestResolveMetaWithEnv(t *testing.T) {
	t.Setenv("EIGENFLUX_HOST", "openclaw/0.0.10")
	t.Setenv("EIGENFLUX_CHANNEL", "feishu")
	m := ResolveMeta()
	if m.Host != "openclaw/0.0.10" {
		t.Errorf("Host = %q, want %q", m.Host, "openclaw/0.0.10")
	}
	if m.Channel != "feishu" {
		t.Errorf("Channel = %q, want %q", m.Channel, "feishu")
	}
}

func TestLoadOrCreateClientID(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("EIGENFLUX_HOME", dir)

	id1 := loadOrCreateClientID()
	if len(id1) != 8 {
		t.Errorf("client_id length = %d, want 8", len(id1))
	}

	// Second call should return the same ID
	id2 := loadOrCreateClientID()
	if id1 != id2 {
		t.Errorf("client_id changed: %q → %q", id1, id2)
	}

	// Verify file was written
	data, err := os.ReadFile(filepath.Join(dir, ".eigenflux", "client_id"))
	if err != nil {
		t.Fatalf("read client_id file: %v", err)
	}
	if got := string(data[:8]); got != id1 {
		t.Errorf("file content = %q, want %q", got, id1)
	}
}

func TestResolveLanguage(t *testing.T) {
	t.Setenv("LANG", "zh_CN.UTF-8")
	t.Setenv("LC_ALL", "")
	got := resolveLanguage()
	if got != "zh-CN" {
		t.Errorf("resolveLanguage() = %q, want %q", got, "zh-CN")
	}
}
