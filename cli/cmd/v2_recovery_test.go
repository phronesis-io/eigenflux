package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/cache"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/controlcontext"
	"cli.eigenflux.ai/internal/profilestate"
)

func TestForcedV2RefreshAdoptsAuthoritativeAgentAndClearsIdentityCaches(t *testing.T) {
	now := time.Now().UnixMilli()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v2/agent-sessions/refresh-challenges":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": map[string]interface{}{
				"nonce": "efn_recovery-test", "issued_at": now, "expires_at": now + 60000,
			}})
		case "/api/v2/agent-sessions/refresh":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": map[string]interface{}{
				"agent_id": "historical-agent", "principal_id": "current-principal",
				"access_token": "new-access", "refresh_token": "new-refresh",
				"expires_at": now + 900000, "scopes": []string{"profile:read", "feed:read"},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	home := tempHome(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	active, err := cfg.GetActive("")
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.UpdateServer(active.Name, server.URL, ""); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := auth.LoadOrCreateIdentity(active.Name); err != nil {
		t.Fatal(err)
	}
	if err := auth.SaveV2Credentials(active.Name, &auth.V2Credentials{
		AgentID: "temporary-agent", PrincipalID: "current-principal", AccessToken: "old-access",
		RefreshToken: "old-refresh", ExpiresAt: now + 900000, Scopes: []string{"onboarding:read"},
	}); err != nil {
		t.Fatal(err)
	}
	serverDir := cache.ServerDir(active.Name)
	if err := os.MkdirAll(serverDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"profile.json", "contacts.json"} {
		if err := os.WriteFile(filepath.Join(serverDir, name), []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, relativePath := range []string{
		filepath.Join("data", "broadcasts", "20260901", "old-feed.json"),
		filepath.Join("data", "messages", "20260901", "old-message.json"),
		filepath.Join("data", "events", "queue.json"),
	} {
		path := filepath.Join(serverDir, relativePath)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(`{"agent_id":"temporary-agent"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := controlcontext.Save(active.Name, controlcontext.Snapshot{OwnerAgentID: "temporary-agent", Revision: 1, Context: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if err := profilestate.Save(home, active.Name, "temporary-agent", profilestate.State{LastRefreshUnix: 1}); err != nil {
		t.Fatal(err)
	}

	refreshed, err := refreshV2Credentials(active.Name, server.URL, true)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.AgentID != "historical-agent" || refreshed.PrincipalID != "current-principal" || refreshed.AccessToken != "new-access" {
		t.Fatalf("credentials were not atomically recalibrated: %+v", refreshed)
	}
	for _, name := range []string{"profile.json", "contacts.json", "control-context.json"} {
		if _, err := os.Stat(filepath.Join(serverDir, name)); !os.IsNotExist(err) {
			t.Fatalf("stale %s remains: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(serverDir, "data")); !os.IsNotExist(err) {
		t.Fatalf("old Agent Feed, message, or pending-event data remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(serverDir, "agent-v2-credentials.json")); err != nil {
		t.Fatalf("refreshed authoritative credentials are missing: %v", err)
	}
	if _, err := os.Stat(profilestate.FilePath(home, active.Name, "temporary-agent")); !os.IsNotExist(err) {
		t.Fatalf("temporary Agent profile state remains: %v", err)
	}
}
