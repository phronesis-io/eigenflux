package cmd

import (
	"os"
	"strconv"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/profilestate"
)

func tempAuthenticatedProfileHome(t *testing.T) string {
	home := tempHome(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	srv, err := cfg.GetActive("")
	if err != nil {
		t.Fatalf("active server: %v", err)
	}
	if err := auth.SaveCredentials(srv.Name, &auth.Credentials{AgentID: "42", AccessToken: "test"}); err != nil {
		t.Fatalf("save test credentials: %v", err)
	}
	return home
}

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
		stamp func() error
		key   string
	}{
		{"write stamp", stampProfileRefreshed, kvProfileRefreshAt},
		{"evaluate stamp", stampProfileChecked, kvProfileRefreshCheckedAt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tempAuthenticatedProfileHome(t)
			if err := tc.stamp(); err != nil {
				t.Fatalf("stamp: %v", err)
			}

			srv, agentID := activeProfileStateScope()
			now := time.Now().Unix()
			state := profilestate.Load(config.HomeDir(), srv, agentID)
			got := state.LastRefreshUnix
			if tc.key == kvProfileRefreshCheckedAt {
				got = state.LastCheckedUnix
			}
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
	tempAuthenticatedProfileHome(t)
	maybePromptProfileRefresh()

	srv, agentID := activeProfileStateScope()
	now := time.Now().Unix()
	if seeded := profilestate.Load(config.HomeDir(), srv, agentID).LastCheckedUnix; seeded <= 0 || seeded > now {
		t.Fatal("first run must seed the checked stamp")
	}
}

// Emitting must not consume any state. Callers that discard stderr (every
// plugin adapter does) would otherwise spend the prompt on a block nobody can
// read, starving the shell-side agent that shares the same config.
func TestMaybePromptSpendsNoStateOnEmit(t *testing.T) {
	home := tempAuthenticatedProfileHome(t)
	srv, agentID := activeProfileStateScope()
	overdue := time.Now().Unix() - int64(profileRefreshStaleAfter/time.Second) - 3600
	if err := profilestate.Save(config.HomeDir(), srv, agentID, profilestate.State{LastRefreshUnix: overdue}); err != nil {
		t.Fatalf("seed overdue stamp: %v", err)
	}
	statePath := profilestate.FilePath(home, srv, agentID)
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}

	for i := 0; i < 3; i++ {
		maybePromptProfileRefresh()
	}

	after, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("re-read config: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("emitting the prompt mutated config:\nbefore: %s\nafter:  %s", before, after)
	}

	// And it must still consider itself overdue, i.e. keep prompting until an
	// actual refresh or evaluation lands.
	now := time.Now().Unix()
	reloaded := profilestate.Load(config.HomeDir(), srv, agentID)
	touch := maxInt64(
		validProfileStamp(reloaded.LastRefreshUnix, now),
		validProfileStamp(reloaded.LastCheckedUnix, now),
	)
	if !shouldPromptProfileRefresh(touch, now) {
		t.Error("prompt went quiet without any refresh or evaluation landing")
	}

	// Evaluating settles it.
	if err := stampProfileChecked(); err != nil {
		t.Fatalf("settle profile state: %v", err)
	}
	settled := profilestate.Load(config.HomeDir(), srv, agentID)
	touch = maxInt64(
		validProfileStamp(settled.LastRefreshUnix, now),
		validProfileStamp(settled.LastCheckedUnix, now),
	)
	if shouldPromptProfileRefresh(touch, now) {
		t.Error("evaluating the profile must settle the prompt")
	}
}
