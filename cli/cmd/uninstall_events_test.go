package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/maintenance"
	"cli.eigenflux.ai/internal/uninstallscript"
	watchstate "cli.eigenflux.ai/internal/watch"
)

func TestUninstallTelemetryAccountIsolationPrivacyAndReplay(t *testing.T) {
	restore := config.UseHomeDir(config.HomeDir())
	defer restore()
	var received []maintenance.Event
	var tokens []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/maintenance/events:batch" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		var req struct {
			Events []maintenance.Event `json:"events"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		received = append(received, req.Events...)
		tokens = append(tokens, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{"accepted_events":2}}`))
	}))
	defer srv.Close()
	o := newUninstallObservation(filepath.Join(t.TempDir(), "eigenflux"), "agent", "other", "私人原因 中文 owner@example.com")
	o.Homes = nil
	for _, id := range []string{"42", "43"} {
		home := filepath.Join(t.TempDir(), ".eigenflux")
		config.SetHomeDir(home)
		cfg := config.Config{DefaultServer: "test", Servers: []config.Server{{Name: "test", Endpoint: srv.URL}}}
		if err := cfg.Save(); err != nil {
			t.Fatal(err)
		}
		if err := auth.SaveV2Credentials("test", &auth.V2Credentials{AgentID: id, AccessToken: "fixture-" + id, RefreshToken: "fixture-refresh", ExpiresAt: time.Now().Add(time.Hour).UnixMilli()}); err != nil {
			t.Fatal(err)
		}
		o.Homes = append(o.Homes, installationHome{Home: home, Host: "codex"})
	}
	o.discoverScopes()
	if len(o.Scopes) != 2 {
		t.Fatalf("scopes=%d", len(o.Scopes))
	}
	o.emit("started", "")
	o.emit("started", "")
	o.finish("removed", "")
	if len(received) != 4 || len(tokens) != 2 || tokens[0] == tokens[1] {
		t.Fatalf("wrong account events: %d, %v", len(received), tokens)
	}
	for _, event := range received {
		if event.AttemptID != o.AttemptID || event.Source != "agent" || event.Phase != "uninstall" {
			t.Fatalf("wrong event %+v", event)
		}
		data, _ := json.Marshal(event)
		if strings.Contains(string(data), "owner@example.com") || strings.Contains(string(data), "/") || strings.Contains(string(data), "私人") {
			t.Fatal("private reason or path leaked")
		}
	}
	for _, entry := range o.Homes {
		data, _ := os.ReadFile(filepath.Join(entry.Home, "uninstall.json"))
		if !strings.Contains(string(data), "私人原因") {
			t.Fatal("optional local reason lost")
		}
	}
	before := len(received)
	config.SetHomeDir(o.Scopes[0].Home)
	if err := auth.SaveV2Credentials("test", &auth.V2Credentials{AgentID: "99", AccessToken: "fixture-99", RefreshToken: "fixture-refresh"}); err != nil {
		t.Fatal(err)
	}
	o.emit("failed", "uninstall_failed")
	o.flush()
	if len(received) != before+1 {
		t.Fatal("account switched queue was uploaded or old identity reused")
	}
}

func TestUninstallOriginalRevalidationAndLocks(t *testing.T) {
	fixture := func(t *testing.T) *uninstallObservation {
		dir := t.TempDir()
		bin := filepath.Join(dir, "original-eigenflux")
		if err := os.WriteFile(bin, []byte("fixture binary"), 0700); err != nil {
			t.Fatal(err)
		}
		home := filepath.Join(dir, ".eigenflux")
		if err := os.MkdirAll(home, 0700); err != nil {
			t.Fatal(err)
		}
		o := newUninstallObservation(bin, "manual", "not_provided", "")
		o.Homes = []installationHome{{Home: home}}
		o.FileDigests = map[string]string{bin: uninstallscript.Digest([]byte("fixture binary"))}
		return o
	}
	t.Run("changed_installation_preserved", func(t *testing.T) {
		o := fixture(t)
		_ = os.WriteFile(o.Executable, []byte("new installation"), 0700)
		if err := o.removeOriginal(); err == nil {
			t.Fatal("changed installation removed")
		}
		if data, _ := os.ReadFile(o.Executable); string(data) != "new installation" {
			t.Fatal("changed installation lost")
		}
	})
	t.Run("active_update_preserved", func(t *testing.T) {
		o := fixture(t)
		release, err := watchstate.AcquirePath(o.Executable + ".update.lock")
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		if err := o.removeOriginal(); err == nil {
			t.Fatal("active update ignored")
		}
		if _, err := os.Stat(o.Executable); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("unchanged_original_removed", func(t *testing.T) {
		o := fixture(t)
		if err := o.removeOriginal(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(o.Executable); !os.IsNotExist(err) {
			t.Fatal("original remains")
		}
	})
	t.Run("unowned_cleanup_path_preserved", func(t *testing.T) {
		o := fixture(t)
		o.CleanupFiles = []string{filepath.Join(filepath.Dir(o.Executable), "unrelated.txt")}
		if err := o.removeOriginal(); err == nil {
			t.Fatal("unowned cleanup accepted")
		}
		if _, err := os.Stat(o.Executable); err != nil {
			t.Fatal("removed before validating all paths")
		}
	})
}
