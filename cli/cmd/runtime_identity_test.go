package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/profilestate"
)

func TestRuntimeMetaUsesEvidenceWithoutInferringMode(t *testing.T) {
	server := profilestate.RuntimeState{Host: "openclaw/2026.9.1", Mode: "plugin"}
	for _, tc := range []struct {
		name       string
		base       client.Meta
		host, mode string
	}{
		{"shell inherits installation", client.Meta{Host: "terminal"}, "openclaw/2026.9.1", "plugin"},
		{"same product retains version", client.Meta{Host: "openclaw"}, "openclaw/2026.9.1", "plugin"},
		{"explicit bare product clears cached version", client.Meta{Host: "openclaw", HostExplicit: true}, "openclaw", "plugin"},
		{"new product discards old mode", client.Meta{Host: "workbuddy/5.5.4", Channel: "plugin"}, "workbuddy/5.5.4", ""},
		{"native task overrides plugin", client.Meta{Host: "openclaw", Mode: "skill"}, "openclaw/2026.9.1", "skill"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveRuntimeMeta(server, tc.base)
			if err != nil || got.Host != tc.host || got.Mode != tc.mode {
				t.Fatalf("meta=%+v err=%v", got, err)
			}
		})
	}
	for _, host := range []string{"codex", "claude-code", "openclaw"} {
		got, err := resolveRuntimeMeta(profilestate.RuntimeState{}, client.Meta{Host: host, Channel: host})
		if err != nil || got.Mode != "" {
			t.Fatalf("product/channel inferred mode: %+v %v", got, err)
		}
	}
}

func runtimeTestConfig(t *testing.T, endpoint string, v2 bool) (*config.Config, string) {
	t.Helper()
	tempHome(t)
	oldMeta, oldVersion, oldServer := clientMeta, version, serverFlag
	clientMeta, version, serverFlag = client.Meta{Host: "terminal", Channel: "cli"}, "0.0.44", ""
	t.Cleanup(func() { clientMeta, version, serverFlag = oldMeta, oldVersion, oldServer })
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	server, err := cfg.GetActive("")
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.UpdateServer(server.Name, endpoint, ""); err != nil {
		t.Fatal(err)
	}
	if err := cfg.SetKV(autoSkillSyncKey, "false"); err != nil {
		t.Fatal(err)
	}
	if v2 {
		err = auth.SaveV2Credentials(server.Name, &auth.V2Credentials{AgentID: "agent-1", AccessToken: "test-v2", RefreshToken: "test-refresh", ExpiresAt: time.Now().Add(time.Hour).UnixMilli()})
	} else {
		err = auth.SaveCredentials(server.Name, &auth.Credentials{AgentID: "agent-1", AccessToken: "test-v1"})
	}
	if err != nil {
		t.Fatal(err)
	}
	return cfg, server.Name
}

func TestRuntimeReportPersistsAcrossRequestsAndRetries(t *testing.T) {
	for _, v2 := range []bool{false, true} {
		t.Run(strconv.FormatBool(v2), func(t *testing.T) {
			puts, failure := 0, false
			var lastHost, lastMode string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				lastHost, lastMode = r.Header.Get("X-Client-Host"), r.Header.Get("X-Client-Mode")
				if r.Method == http.MethodPut {
					puts++
				}
				if failure {
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = w.Write([]byte(`{"code":1,"msg":"retry later"}`))
					return
				}
				_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
			}))
			defer server.Close()
			cfg, serverName := runtimeTestConfig(t, server.URL, v2)
			result, err := reportRuntimeSettings(cfg, "skill", "", "hermes", "0.17.0", false)
			if err != nil || result.Status != "reported" || puts != 1 {
				t.Fatalf("initial=%+v err=%v puts=%d", result, err, puts)
			}
			// Reload the saved Home and create a fresh HTTP client, as a later CLI process does.
			cfg, _ = config.Load()
			c, err := newSettingsClient(cfg, serverName)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.Get("/probe", nil); err != nil {
				t.Fatal(err)
			}
			if lastHost != "hermes/0.17.0" || lastMode != "skill" {
				t.Fatalf("later headers host=%q mode=%q", lastHost, lastMode)
			}
			result, err = reportRuntimeSettings(cfg, "", "", "", "", false)
			if err != nil || result.Status != "unchanged" || puts != 1 {
				t.Fatalf("dedup=%+v err=%v puts=%d", result, err, puts)
			}
			if _, err := profilestate.UpdateRuntime(config.HomeDir(), serverName, func(state *profilestate.RuntimeState) (bool, error) {
				state.ReportedAtMillis = time.Now().Add(-25 * time.Hour).UnixMilli()
				return true, nil
			}); err != nil {
				t.Fatal(err)
			}
			result, err = reportRuntimeSettings(cfg, "", "", "", "", false)
			if err != nil || result.Status != "reported" || puts != 2 {
				t.Fatalf("daily=%+v err=%v puts=%d", result, err, puts)
			}
			version = "0.0.45"
			result, err = reportRuntimeSettings(cfg, "", "", "", "", false)
			if err != nil || result.Status != "reported" || puts != 3 {
				t.Fatalf("upgrade=%+v err=%v puts=%d", result, err, puts)
			}
			before := loadRuntimeTestState(t, serverName)
			stamp, snapshot := before.ReportedAtMillis, before.ReportedSnapshot
			failure = true
			result, err = reportRuntimeSettings(cfg, "skill", "", "workbuddy", "5.5.4", false)
			if err == nil || result.Status != "failed" || result.Error == "" {
				t.Fatalf("failure=%+v err=%v", result, err)
			}
			cfg, _ = config.Load()
			if got := loadRuntimeTestState(t, serverName).ReportedAtMillis; got != stamp {
				t.Fatal("failed report changed successful timestamp")
			}
			if got := loadRuntimeTestState(t, serverName).ReportedSnapshot; got != snapshot {
				t.Fatal("failed report changed successful snapshot")
			}
			failure = false
			result, err = reportRuntimeSettings(cfg, "", "", "", "", false)
			if err != nil || result.Status != "reported" || lastHost != "workbuddy/5.5.4" {
				t.Fatalf("retry=%+v err=%v host=%q", result, err, lastHost)
			}
			result, err = reportRuntimeSettings(cfg, "", "", "workbuddy", "", false)
			if err != nil || result.Status != "reported" || lastHost != "workbuddy" {
				t.Fatalf("explicit clear=%+v err=%v host=%q", result, err, lastHost)
			}
		})
	}
}

func TestRuntimeReportUnknownAndInvalidNeverInventIdentity(t *testing.T) {
	for _, name := range []string{"terminal", "plugin", "skill", "skills", "cli", "cli-direct", "unknown"} {
		if _, err := reportedRuntimeHost(name, ""); err == nil {
			t.Fatalf("accepted mode/sentinel as product: %s", name)
		}
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
	}))
	defer server.Close()
	cfg, _ := runtimeTestConfig(t, server.URL, true)
	result, err := reportRuntimeSettings(cfg, "", "", "", "", false)
	if err != nil || result.Status != "missing" || requests != 0 {
		t.Fatalf("unknown=%+v err=%v requests=%d", result, err, requests)
	}
	for _, params := range [][3]string{{"plugins", "hermes", ""}, {"skill", "terminal", ""}, {"skill", "", "0.17"}} {
		result, err = reportRuntimeSettings(cfg, params[0], "", params[1], params[2], false)
		if err == nil || result.Status != "failed" || requests != 0 {
			t.Fatalf("invalid=%+v err=%v requests=%d", result, err, requests)
		}
	}
}

func TestRuntimeReportAccountAndTransportChangesBypassCache(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
	}))
	defer server.Close()
	cfg, serverName := runtimeTestConfig(t, server.URL, false)
	if result, err := reportRuntimeSettings(cfg, "skill", "", "codex", "", false); err != nil || result.Status != "reported" {
		t.Fatalf("initial=%+v %v", result, err)
	}
	credentials := &auth.V2Credentials{AgentID: "agent-1", AccessToken: "test-v2", RefreshToken: "test-refresh", ExpiresAt: time.Now().Add(time.Hour).UnixMilli()}
	if err := auth.SaveV2Credentials(serverName, credentials); err != nil {
		t.Fatal(err)
	}
	if result, err := reportRuntimeSettings(cfg, "", "", "", "", false); err != nil || result.Status != "reported" {
		t.Fatalf("transport=%+v %v", result, err)
	}
	credentials.AgentID = "agent-2"
	if err := auth.SaveV2Credentials(serverName, credentials); err != nil {
		t.Fatal(err)
	}
	if result, err := reportRuntimeSettings(cfg, "", "", "", "", false); err != nil || result.Status != "reported" {
		t.Fatalf("account=%+v %v", result, err)
	}
	if len(paths) != 3 || paths[0] != "/api/v1/agents/me/settings" || paths[1] != "/api/v2/agents/me/settings" {
		t.Fatalf("requests=%v", paths)
	}
}

func TestHeartbeatPlanReportsBeforeSkillsFailure(t *testing.T) {
	reports := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && r.URL.Path == "/api/v2/agents/me/settings" {
			reports++
			_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	cfg, serverName := runtimeTestConfig(t, server.URL, true)
	if _, err := configureRuntimeIdentity(cfg, serverName, "skill", "hermes", "0.17.0"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EIGENFLUX_SKILLS_DIR", t.TempDir())
	t.Setenv("EIGENFLUX_CDN_URL", server.URL)
	if err := heartbeatPlanCmd.RunE(heartbeatPlanCmd, nil); err == nil {
		t.Fatal("expected unavailable Skills error")
	}
	if reports != 1 {
		t.Fatalf("heartbeat report calls=%d", reports)
	}
}

func TestRuntimeIdentityIsServerScoped(t *testing.T) {
	cfg, serverName := runtimeTestConfig(t, "http://127.0.0.1:1", false)
	if _, err := configureRuntimeIdentity(cfg, serverName, "skill", "hermes", "0.17.0"); err != nil {
		t.Fatal(err)
	}
	other := &config.Server{Name: "other"}
	if meta := clientMetaForServer(other); meta.Host != "" || meta.Mode != "" {
		t.Fatalf("identity leaked across servers: %+v", meta)
	}
}

func TestFeedBothTransportsReportWithoutMemoryAndSurviveReportFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		v2     bool
		action string
	}{
		{"v1-refresh", false, "refresh"},
		{"v2-refresh", true, "refresh"},
		{"v2-more", true, "more"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reports, syncs := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case strings.HasSuffix(r.URL.Path, "/agent-context"):
					w.WriteHeader(http.StatusConflict)
					_, _ = w.Write([]byte(`{"error":{"code":"ONBOARDING_REQUIRED","message":"baseline"}}`))
				case strings.HasSuffix(r.URL.Path, "/agents/me/settings") && r.Method == http.MethodPut:
					reports++
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = w.Write([]byte(`{"code":1,"msg":"metadata unavailable"}`))
				case strings.HasSuffix(r.URL.Path, "/agents/me/settings"):
					syncs++
					_, _ = w.Write([]byte(`{"code":0,"data":{"feed_poll_interval":7200}}`))
				case strings.HasSuffix(r.URL.Path, "/feed"):
					_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 0, "data": map[string]interface{}{"schema_version": "feed.v2", "personalization": map[string]interface{}{"mode": "baseline"}, "items": []interface{}{}}})
				default:
					_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
				}
			}))
			defer server.Close()
			cfg, serverName := runtimeTestConfig(t, server.URL, tc.v2)
			if _, err := configureRuntimeIdentity(cfg, serverName, "skill", "hermes", "0.17.0"); err != nil {
				t.Fatal(err)
			}
			for flag, value := range map[string]string{"action": tc.action, "cursor": ""} {
				oldValue := feedPollCmd.Flags().Lookup(flag).Value.String()
				if err := feedPollCmd.Flags().Set(flag, value); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = feedPollCmd.Flags().Set(flag, oldValue) })
			}
			oldFormat := formatFlag
			formatFlag = "json"
			t.Cleanup(func() { formatFlag = oldFormat })
			if err := feedPollCmd.RunE(feedPollCmd, nil); err != nil {
				t.Fatalf("successful feed blocked by metadata: %v", err)
			}
			if reports != 1 || syncs != 1 {
				t.Fatalf("reports=%d syncs=%d", reports, syncs)
			}
			cfg, _ = config.Load()
			if got := loadRuntimeTestState(t, serverName).ReportedAtMillis; got != 0 {
				t.Fatal("failed metadata got success stamp")
			}
		})
	}
}

func TestRuntimeReportPreservesRemotePreferences(t *testing.T) {
	for _, v2 := range []bool{false, true} {
		for _, local := range []string{"", "stale local preference"} {
			t.Run(strconv.FormatBool(v2)+local, func(t *testing.T) {
				remote := "owner changed preference in Console"
				requests := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodPut {
						requests++
						var body map[string]interface{}
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
							t.Error(err)
						}
						if pref, exists := body["feed_delivery_preference"]; exists {
							remote, _ = pref.(string)
							t.Error("runtime observation must omit shared preferences")
						}
						_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
					} else {
						_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 0, "data": map[string]interface{}{"feed_delivery_preference": remote}})
					}
				}))
				defer server.Close()
				cfg, _ := runtimeTestConfig(t, server.URL, v2)
				if err := cfg.SetKV(settingsSyncedKey, "1"); err != nil {
					t.Fatal(err)
				}
				if err := cfg.SetKV("feed_delivery_preference", local); err != nil {
					t.Fatal(err)
				}
				result, err := reportRuntimeSettings(cfg, "skill", "", "codex", "", false)
				if err != nil || result.Status != "reported" {
					t.Fatalf("result=%+v err=%v", result, err)
				}
				if err := SyncSettings(cfg); err != nil {
					t.Fatal(err)
				}
				if requests != 1 || remote != "owner changed preference in Console" || cfg.GetKV("feed_delivery_preference") != remote {
					t.Fatalf("requests=%d remote=%q local=%q", requests, remote, cfg.GetKV("feed_delivery_preference"))
				}
			})
		}
	}
}

func loadRuntimeTestState(t *testing.T, serverName string) profilestate.RuntimeState {
	t.Helper()
	state, err := profilestate.LoadRuntime(config.HomeDir(), serverName)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestRuntimeReportExplicitEnvironmentClearsCachedPluginVersion(t *testing.T) {
	for _, v2 := range []bool{false, true} {
		t.Run(strconv.FormatBool(v2), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.Header.Get("X-Client-Host"); got != "openclaw" {
					t.Errorf("host=%q, want bare product", got)
				}
				if got := r.Header.Get("X-Client-Plugin-Version"); got != "0.2.1" {
					t.Errorf("plugin version=%q", got)
				}
				_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
			}))
			defer server.Close()
			cfg, serverName := runtimeTestConfig(t, server.URL, v2)
			if _, err := configureRuntimeIdentity(cfg, serverName, "plugin", "openclaw", "0.1.0"); err != nil {
				t.Fatal(err)
			}
			t.Setenv("EIGENFLUX_HOST", "openclaw")
			t.Setenv("EIGENFLUX_MODE", "plugin")
			t.Setenv("EIGENFLUX_PLUGIN_VERSION", "0.2.1")
			clientMeta = client.ResolveMeta()
			result, err := reportRuntimeSettings(cfg, "", "", "", "", false)
			if err != nil || result.Status != "reported" {
				t.Fatalf("report=%+v err=%v", result, err)
			}
			if saved := loadRuntimeTestState(t, serverName); saved.Host != "openclaw" {
				t.Fatalf("saved host retained plugin version: %+v", saved)
			}
		})
	}
}

func TestV1SettingsSuccessRefreshesCredentialExpiry(t *testing.T) {
	for _, operation := range []string{"report", "sync"} {
		t.Run(operation, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
			}))
			defer server.Close()
			cfg, serverName := runtimeTestConfig(t, server.URL, false)
			creds, err := auth.LoadCredentials(serverName)
			if err != nil {
				t.Fatal(err)
			}
			creds.ExpiresAt = time.Now().Add(time.Hour).UnixMilli()
			if err := auth.SaveCredentials(serverName, creds); err != nil {
				t.Fatal(err)
			}
			if operation == "report" {
				if _, err := reportRuntimeSettings(cfg, "skill", "", "codex", "", false); err != nil {
					t.Fatal(err)
				}
			} else if err := SyncSettings(cfg); err != nil {
				t.Fatal(err)
			}
			refreshed, err := auth.LoadCredentials(serverName)
			if err != nil {
				t.Fatal(err)
			}
			if refreshed.ExpiresAt < time.Now().Add(29*24*time.Hour).UnixMilli() {
				t.Fatalf("settings did not extend V1 session: before=%d after=%d", creds.ExpiresAt, refreshed.ExpiresAt)
			}
		})
	}
}
