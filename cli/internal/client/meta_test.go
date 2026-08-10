package client

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveMeta(t *testing.T) {
	clearRuntimeEnv(t)
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
	if m.Model != "" {
		t.Errorf("Model = %q, want empty (no default)", m.Model)
	}
}

func TestResolveMetaWithEnv(t *testing.T) {
	clearRuntimeEnv(t)
	t.Setenv("EIGENFLUX_HOST", "openclaw/0.0.10")
	t.Setenv("EIGENFLUX_CHANNEL", "feishu")
	t.Setenv("EIGENFLUX_MODEL", "claude-opus-4-8")
	m := ResolveMeta()
	if m.Host != "openclaw/0.0.10" {
		t.Errorf("Host = %q, want %q", m.Host, "openclaw/0.0.10")
	}
	if m.Channel != "feishu" {
		t.Errorf("Channel = %q, want %q", m.Channel, "feishu")
	}
	if m.Model != "claude-opus-4-8" {
		t.Errorf("Model = %q, want %q", m.Model, "claude-opus-4-8")
	}
}

func TestResolveMetaDetectsWorkBuddy(t *testing.T) {
	clearRuntimeEnv(t)
	t.Setenv("WORKBUDDY_APP_NAME", "WorkBuddy")
	t.Setenv("WORKBUDDY_APP_VERSION", "5.3.8")
	if got := ResolveMeta().Host; got != "workbuddy/5.3.8" {
		t.Fatalf("Host = %q, want workbuddy/5.3.8", got)
	}
}

func TestResolveMetaExplicitHostWinsOverWorkBuddy(t *testing.T) {
	clearRuntimeEnv(t)
	t.Setenv("WORKBUDDY_PRODUCT_NAME", "WorkBuddy")
	t.Setenv("WORKBUDDY_APP_VERSION", "5.3.8")
	t.Setenv("EIGENFLUX_HOST", "hermes/0.20.0")
	if got := ResolveMeta().Host; got != "hermes/0.20.0" {
		t.Fatalf("Host = %q, want explicit hermes identity", got)
	}
}

func TestResolveMetaWorkBuddyRejectsUnsafeVersion(t *testing.T) {
	clearRuntimeEnv(t)
	t.Setenv("CODEBUDDY_HOST", "workbuddy-desktop")
	t.Setenv("CLIENT_INFO_PRODUCT_VERSION", "5.3.8\nforged")
	if got := ResolveMeta().Host; got != "workbuddy" {
		t.Fatalf("Host = %q, want name-only workbuddy", got)
	}
}

func TestResolveMetaDoesNotMixWorkBuddyVersionSources(t *testing.T) {
	clearRuntimeEnv(t)
	t.Setenv("CODEBUDDY_HOST", "workbuddy-desktop")
	t.Setenv("CLIENT_INFO_PRODUCT_NAME", "AnotherProduct")
	t.Setenv("CLIENT_INFO_PRODUCT_VERSION", "9.9.9")
	if got := ResolveMeta().Host; got != "workbuddy" {
		t.Fatalf("Host = %q, want name-only workbuddy", got)
	}
}

func clearRuntimeEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"EIGENFLUX_HOST",
		"WORKBUDDY_APP_NAME",
		"WORKBUDDY_APP_VERSION",
		"WORKBUDDY_PRODUCT_NAME",
		"CLIENT_INFO_PRODUCT_NAME",
		"CLIENT_INFO_PRODUCT_VERSION",
		"CODEBUDDY_HOST",
	} {
		t.Setenv(key, "")
	}
}

func TestMetaSetHeadersModel(t *testing.T) {
	h := http.Header{}
	Meta{Model: "claude-opus-4-8"}.SetHeaders(h)
	if got := h.Get("X-Client-Model"); got != "claude-opus-4-8" {
		t.Errorf("X-Client-Model = %q, want %q", got, "claude-opus-4-8")
	}
	// Empty model must not emit the header.
	h2 := http.Header{}
	Meta{}.SetHeaders(h2)
	if _, ok := h2["X-Client-Model"]; ok {
		t.Error("X-Client-Model should be absent when Model is empty")
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
