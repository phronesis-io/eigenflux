package cmd

import (
	"strconv"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/config"
)

func TestShouldPromptProfileRefresh(t *testing.T) {
	now := time.Now().Unix()
	h := int64(3600)
	stale := int64(profileRefreshStaleAfter / time.Second)
	cool := int64(refreshPromptCooldown / time.Second)

	cases := []struct {
		name         string
		lastTouch    int64
		lastPrompted int64
		want         bool
	}{
		// No usable stamp never prompts — the caller seeds instead, so a CLI
		// upgrade doesn't nag every agent at once.
		{"no stamp", 0, 0, false},
		{"no stamp, prompted long ago", 0, now - 100*h, false},

		{"touched 1h ago", now - h, 0, false},
		{"touched just inside the window", now - (stale - 1), 0, false},
		{"touched exactly at the window", now - stale, 0, true},
		{"touched past the window", now - (stale + h), 0, true},

		{"stale but prompted 1h ago", now - (stale + h), now - h, false},
		{"stale, prompted just inside cooldown", now - (stale + h), now - (cool - 1), false},
		{"stale, prompted exactly at cooldown", now - (stale + h), now - cool, true},
		{"stale, prompted long ago", now - (stale + h), now - 100*h, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shouldPromptProfileRefresh(c.lastTouch, c.lastPrompted, now); got != c.want {
				t.Errorf("shouldPromptProfileRefresh(%d, %d) = %v, want %v",
					c.lastTouch, c.lastPrompted, got, c.want)
			}
		})
	}
}

// serverKVUnix must reject values it cannot trust rather than let them decide
// the prompt: a future stamp would silence it until wall time caught up.
func TestServerKVUnixRejectsUntrustedValues(t *testing.T) {
	tempHome(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	srv, err := cfg.GetActive("")
	if err != nil {
		t.Fatalf("active server: %v", err)
	}
	now := time.Now().Unix()

	for _, c := range []struct {
		name  string
		value string
		want  int64
	}{
		{"valid past stamp", strconv.FormatInt(now-3600, 10), now - 3600},
		{"future stamp", strconv.FormatInt(now+3600, 10), 0},
		{"zero", "0", 0},
		{"negative", "-1", 0},
		{"not a number", "yesterday", 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			if err := cfg.SetServerKV(srv.Name, kvProfileRefreshAt, c.value); err != nil {
				t.Fatalf("set kv: %v", err)
			}
			if got := serverKVUnix(cfg, srv.Name, kvProfileRefreshAt, now); got != c.want {
				t.Errorf("serverKVUnix(%q) = %d, want %d", c.value, got, c.want)
			}
		})
	}
}

// The stamps must survive a config round-trip under the server scope the
// reader uses — a scope mismatch would compile fine and silently nag forever.
func TestStampsRoundTripAndSuppressPrompt(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stamp func()
		key   string
	}{
		{"write stamp", stampProfileRefreshed, kvProfileRefreshAt},
		{"evaluate stamp", stampProfileChecked, kvProfileRefreshCheckedAt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tempHome(t)
			tc.stamp()

			cfg, err := config.Load()
			if err != nil {
				t.Fatalf("reload config: %v", err)
			}
			srv, err := cfg.GetActive("")
			if err != nil {
				t.Fatalf("active server: %v", err)
			}
			now := time.Now().Unix()
			got := serverKVUnix(cfg, srv.Name, tc.key, now)
			if got <= 0 {
				t.Fatalf("%s not persisted under the server scope the reader uses", tc.key)
			}
			if shouldPromptProfileRefresh(got, 0, now) {
				t.Errorf("a fresh %s must suppress the prompt", tc.key)
			}
		})
	}
}

// First run seeds the clock instead of prompting.
func TestMaybePromptSeedsOnFirstRun(t *testing.T) {
	tempHome(t)
	maybePromptProfileRefresh()

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	srv, err := cfg.GetActive("")
	if err != nil {
		t.Fatalf("active server: %v", err)
	}
	now := time.Now().Unix()
	if seeded := serverKVUnix(cfg, srv.Name, kvProfileRefreshCheckedAt, now); seeded <= 0 {
		t.Fatal("first run must seed the checked stamp")
	}
	if prompted := serverKVUnix(cfg, srv.Name, kvProfileRefreshPromptAt, now); prompted != 0 {
		t.Errorf("first run must not prompt, got prompt stamp %d", prompted)
	}
}

// Once overdue the prompt fires and records the cooldown; the next call inside
// that cooldown must stay quiet.
func TestMaybePromptWritesCooldownThenStaysQuiet(t *testing.T) {
	tempHome(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	srv, err := cfg.GetActive("")
	if err != nil {
		t.Fatalf("active server: %v", err)
	}
	overdue := time.Now().Unix() - int64(profileRefreshStaleAfter/time.Second) - 3600
	if err := cfg.SetServerKV(srv.Name, kvProfileRefreshAt, strconv.FormatInt(overdue, 10)); err != nil {
		t.Fatalf("seed overdue stamp: %v", err)
	}

	maybePromptProfileRefresh()

	reloaded, err := config.Load()
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	now := time.Now().Unix()
	first := serverKVUnix(reloaded, srv.Name, kvProfileRefreshPromptAt, now)
	if first <= 0 {
		t.Fatal("overdue profile must prompt and record the cooldown")
	}

	maybePromptProfileRefresh()

	again, err := config.Load()
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if second := serverKVUnix(again, srv.Name, kvProfileRefreshPromptAt, now); second != first {
		t.Errorf("second call inside cooldown rewrote the stamp: %d -> %d", first, second)
	}
}
