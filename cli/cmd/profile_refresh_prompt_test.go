package cmd

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/config"
)

func TestShouldPromptProfileRefresh(t *testing.T) {
	now := time.Now().Unix()
	h := int64(3600)
	stale := int64(profileRefreshStaleAfter / time.Second)

	cases := []struct {
		name      string
		lastTouch int64
		want      bool
	}{
		// No usable stamp never prompts — the caller seeds instead, so a CLI
		// upgrade doesn't nag every agent at once.
		{"no stamp", 0, false},
		{"negative stamp", -1, false},

		{"touched 1h ago", now - h, false},
		{"touched just inside the window", now - (stale - 1), false},
		{"touched exactly at the window", now - stale, true},
		{"touched past the window", now - (stale + h), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shouldPromptProfileRefresh(c.lastTouch, now); got != c.want {
				t.Errorf("shouldPromptProfileRefresh(%d) = %v, want %v", c.lastTouch, got, c.want)
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
			if shouldPromptProfileRefresh(got, now) {
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
}

// Emitting must not consume any state. Callers that discard stderr (every
// plugin adapter does) would otherwise spend the prompt on a block nobody can
// read, starving the shell-side agent that shares the same config.
func TestMaybePromptSpendsNoStateOnEmit(t *testing.T) {
	home := tempHome(t)
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
	before, err := os.ReadFile(filepath.Join(home, "config.json"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}

	for i := 0; i < 3; i++ {
		maybePromptProfileRefresh()
	}

	after, err := os.ReadFile(filepath.Join(home, "config.json"))
	if err != nil {
		t.Fatalf("re-read config: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("emitting the prompt mutated config:\nbefore: %s\nafter:  %s", before, after)
	}

	// And it must still consider itself overdue, i.e. keep prompting until an
	// actual refresh or evaluation lands.
	reloaded, err := config.Load()
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	now := time.Now().Unix()
	touch := maxInt64(
		serverKVUnix(reloaded, srv.Name, kvProfileRefreshAt, now),
		serverKVUnix(reloaded, srv.Name, kvProfileRefreshCheckedAt, now),
	)
	if !shouldPromptProfileRefresh(touch, now) {
		t.Error("prompt went quiet without any refresh or evaluation landing")
	}

	// Evaluating settles it.
	stampProfileChecked()
	settled, err := config.Load()
	if err != nil {
		t.Fatalf("reload after check: %v", err)
	}
	touch = maxInt64(
		serverKVUnix(settled, srv.Name, kvProfileRefreshAt, now),
		serverKVUnix(settled, srv.Name, kvProfileRefreshCheckedAt, now),
	)
	if shouldPromptProfileRefresh(touch, now) {
		t.Error("evaluating the profile must settle the prompt")
	}
}
