package cmd

import (
	"testing"

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
		})
	}
}

// TestSyncedSettingsBody_OtherKeysUnaffected confirms the intent guard is scoped
// to feed_poll_interval and leaves the other synced keys behaving as before.
func TestSyncedSettingsBody_OtherKeysUnaffected(t *testing.T) {
	cfg := &config.Config{KV: map[string]string{
		"recurring_publish":        "true",
		"auto_reply_pm":            "false",
		"feed_delivery_preference": "Push urgent signals",
	}}
	body := syncedSettingsBody(cfg)
	if body["recurring_publish"] != true {
		t.Errorf("recurring_publish = %v, want true", body["recurring_publish"])
	}
	if body["auto_reply_pm"] != false {
		t.Errorf("auto_reply_pm = %v, want false", body["auto_reply_pm"])
	}
	if body["feed_delivery_preference"] != "Push urgent signals" {
		t.Errorf("feed_delivery_preference = %v", body["feed_delivery_preference"])
	}
}
