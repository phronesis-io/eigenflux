package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/config"
)

// TestSyncedSettingsBody_FeedPollIntentGuard verifies that feed_poll_interval is
// pushed to the backend only when the user explicitly set it (an intent marker
// is present), and never when the KV merely holds a value pulled down from the
// backend onboarding ramp. Without this guard, a ramp value echoed back up would
// be recorded as a user override and freeze the ramp.
func TestSyncedSettingsBody_FeedPollIntentGuard(t *testing.T) {
	tests := []struct {
		name      string
		kv        map[string]string
		wantKey   bool // feed_poll_interval present in body
		wantValue int
	}{
		{
			name:    "ramp value pulled down, no intent -> not pushed",
			kv:      map[string]string{"feed_poll_interval": "3600"},
			wantKey: false,
		},
		{
			name:      "user intent set -> pushed with intent value",
			kv:        map[string]string{"feed_poll_interval": "3600", feedPollIntentKey: "1800"},
			wantKey:   true,
			wantValue: 1800,
		},
		{
			name:      "intent only, no mirrored value yet -> pushed",
			kv:        map[string]string{feedPollIntentKey: "900"},
			wantKey:   true,
			wantValue: 900,
		},
		{
			name:    "no interval at all -> absent",
			kv:      map[string]string{"recurring_publish": "true"},
			wantKey: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{KV: tt.kv}
			body := syncedSettingsBody(cfg)
			v, ok := body["feed_poll_interval"]
			if ok != tt.wantKey {
				t.Fatalf("feed_poll_interval present = %v, want %v (body=%v)", ok, tt.wantKey, body)
			}
			if tt.wantKey && v.(int) != tt.wantValue {
				t.Fatalf("feed_poll_interval = %v, want %d", v, tt.wantValue)
			}
			// The user_set override flag must travel with the value and never
			// without it, so the backend pins only genuine intents.
			flag, hasFlag := body["feed_poll_interval_user_set"]
			if hasFlag != tt.wantKey {
				t.Fatalf("feed_poll_interval_user_set present = %v, want %v (body=%v)", hasFlag, tt.wantKey, body)
			}
			if tt.wantKey && flag != true {
				t.Fatalf("feed_poll_interval_user_set = %v, want true", flag)
			}
		})
	}
}

// TestSyncedSettingsBody_OtherKeysUnaffected confirms the intent guard is scoped
// to feed_poll_interval and leaves the other synced keys behaving as before.
func TestSyncedSettingsBody_OtherKeysUnaffected(t *testing.T) {
	cfg := &config.Config{KV: map[string]string{
		"recurring_publish":        "true",
		"auto_reply_pm":            "false",
		"auto_comment":             "false",
		"feed_delivery_preference": "Push urgent signals",
	}}
	body := syncedSettingsBody(cfg)
	if body["recurring_publish"] != true {
		t.Errorf("recurring_publish = %v, want true", body["recurring_publish"])
	}
	if body["auto_reply_pm"] != false {
		t.Errorf("auto_reply_pm = %v, want false", body["auto_reply_pm"])
	}
	if body["auto_comment"] != false {
		t.Errorf("auto_comment = %v, want false", body["auto_comment"])
	}
	if body["feed_delivery_preference"] != "Push urgent signals" {
		t.Errorf("feed_delivery_preference = %v", body["feed_delivery_preference"])
	}
	// The override flag is scoped to feed_poll_interval and must not leak in
	// when no interval intent is being pushed.
	if _, ok := body["feed_poll_interval_user_set"]; ok {
		t.Errorf("feed_poll_interval_user_set present without an interval intent (body=%v)", body)
	}
}

func TestReportedRuntimeHost(t *testing.T) {
	for _, tt := range []struct {
		name    string
		version string
		want    string
		wantErr bool
	}{
		{name: "Hermes", version: "0.20.0", want: "hermes/0.20.0"},
		{name: "workbuddy", want: "workbuddy"},
		{name: "", version: "", want: ""},
		{name: "", version: "1.0.0", wantErr: true},
		{name: "terminal", wantErr: true},
		{name: "bad host", wantErr: true},
		{name: "hermes", version: "1.0\nforged", wantErr: true},
	} {
		t.Run(tt.name+"/"+tt.version, func(t *testing.T) {
			got, err := reportedRuntimeHost(tt.name, tt.version)
			if (err != nil) != tt.wantErr {
				t.Fatalf("reportedRuntimeHost() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("reportedRuntimeHost() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSettingsPushRegistersRuntimeIdentityFlags(t *testing.T) {
	for _, name := range []string{"runtime-name", "runtime-version"} {
		if settingsPushCmd.Flags().Lookup(name) == nil {
			t.Fatalf("settings push missing --%s", name)
		}
	}
}

func TestReportedSettingsSnapshotChangesWithRuntimeIdentity(t *testing.T) {
	hermes := reportedSettingsSnapshot("101", "skill", "", "gpt-5.6", "hermes/0.20.0")
	workbuddy := reportedSettingsSnapshot("101", "skill", "", "gpt-5.6", "workbuddy/5.3.8")
	if hermes == workbuddy {
		t.Fatal("runtime identity change must invalidate the reported-settings snapshot")
	}
}

func TestReportedSettingsSnapshotChangesWithAccount(t *testing.T) {
	first := reportedSettingsSnapshot("101", "skill", "", "gpt-5.6", "hermes/0.20.0")
	second := reportedSettingsSnapshot("202", "skill", "", "gpt-5.6", "hermes/0.20.0")
	if first == second {
		t.Fatal("account switch must invalidate the reported-settings snapshot")
	}
}

func TestPushReportedSendsRuntimeToBoundServerAndCachesSuccess(t *testing.T) {
	requestSeen := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/agents/me/settings" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("authorization = %q", got)
		}
		if got := r.Header.Get("X-Client-Host"); got != "hermes/0.20.0" {
			t.Errorf("X-Client-Host = %q", got)
		}
		if got := r.Header.Get("X-Client-Model"); got != "gpt-5.6" {
			t.Errorf("X-Client-Model = %q", got)
		}
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["mode"] != "skill" {
			t.Errorf("mode = %v", body["mode"])
		}
		requestSeen <- struct{}{}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":null}`))
	}))
	defer server.Close()

	tempHome(t)
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
	if err := auth.SaveCredentials(active.Name, &auth.Credentials{AgentID: "42", AccessToken: "test-token"}); err != nil {
		t.Fatal(err)
	}
	oldMeta := clientMeta
	clientMeta = client.Meta{Host: "terminal", Channel: "cli"}
	t.Cleanup(func() { clientMeta = oldMeta })

	if err := pushReported(cfg, "skill", "gpt-5.6", "hermes", "0.20.0", false); err != nil {
		t.Fatal(err)
	}
	<-requestSeen
	want := reportedSettingsSnapshot("42", "skill", "", "gpt-5.6", "hermes/0.20.0")
	if got, ok, err := cfg.GetServerOnlyKV(active.Name, settingsReportedKey); err != nil || !ok || got != want {
		t.Fatalf("cached snapshot = %q, %v, %v; want %q", got, ok, err, want)
	}
}
