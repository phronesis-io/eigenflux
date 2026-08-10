package cmd

import (
	"errors"
	"strconv"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/profilestate"
)

type failingProfilePromptWriter struct{}

func (failingProfilePromptWriter) Write([]byte) (int, error) {
	return 0, errors.New("closed stderr")
}

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

func TestPluginOwnsProfileRefresh(t *testing.T) {
	for _, tc := range []struct {
		host    string
		channel string
		want    bool
	}{
		{"openclaw/0.0.30", "openclaw", true},
		{"openclaw/0.0.30", "telegram", true},
		{"claude-code/0.0.9", "claude-code", true},
		{"codex/0.0.30", "codex", true},
		{"codex/0.0.30", "skill", false},
		{"codex/0.0.30", "cli", false},
		{"workbuddy/5.3.8", "cli", false},
		{"hermes/0.20.0", "cli", false},
		{"terminal", "cli", false},
	} {
		if got := pluginOwnsProfileRefresh(tc.host, tc.channel); got != tc.want {
			t.Errorf("pluginOwnsProfileRefresh(%q, %q) = %v, want %v", tc.host, tc.channel, got, tc.want)
		}
	}
}

func TestPluginPollDoesNotConsumeProfilePromptCooldown(t *testing.T) {
	tempAuthenticatedProfileHome(t)
	srv, agentID := activeProfileStateScope()
	overdue := time.Now().Unix() - int64(profileRefreshStaleAfter/time.Second) - 3600
	if err := profilestate.Save(config.HomeDir(), srv, agentID, profilestate.State{LastRefreshUnix: overdue}); err != nil {
		t.Fatalf("seed overdue stamp: %v", err)
	}

	oldMeta := clientMeta
	clientMeta = client.Meta{Host: "openclaw/0.0.30", Channel: "openclaw"}
	t.Cleanup(func() { clientMeta = oldMeta })
	maybePromptProfileRefreshFor(srv, agentID)

	if got := profilestate.Load(config.HomeDir(), srv, agentID).LastPromptedUnix; got != 0 {
		t.Fatalf("plugin poll consumed shell prompt cooldown: %d", got)
	}
}

func TestFailedPromptWriteLeavesOnlyShortClaim(t *testing.T) {
	tempAuthenticatedProfileHome(t)
	srv, agentID := activeProfileStateScope()
	now := time.Now().Unix()
	overdue := now - int64(profileRefreshStaleAfter/time.Second) - 3600
	if err := profilestate.Save(config.HomeDir(), srv, agentID, profilestate.State{LastRefreshUnix: overdue}); err != nil {
		t.Fatalf("seed overdue stamp: %v", err)
	}

	oldWriter := profilePromptWriter
	profilePromptWriter = failingProfilePromptWriter{}
	t.Cleanup(func() { profilePromptWriter = oldWriter })
	maybePromptProfileRefresh()

	got := profilestate.Load(config.HomeDir(), srv, agentID).LastPromptedUnix
	wantApprox := now - int64((profilePromptCooldown-profilePromptClaimLease)/time.Second)
	if got < wantApprox-1 || got > wantApprox+1 {
		t.Fatalf("failed delivery stored long cooldown: got %d, want short claim around %d", got, wantApprox)
	}
}

func TestShouldPromptProfileRefresh(t *testing.T) {
	now := time.Now().Unix()
	h := int64(3600)
	stale := int64(profileRefreshStaleAfter / time.Second)

	cases := []struct {
		name         string
		lastTouch    int64
		lastPrompted int64
		want         bool
	}{
		// No usable stamp never prompts — the caller seeds instead, so a CLI
		// upgrade doesn't nag every agent at once.
		{"no stamp", 0, 0, false},
		{"negative stamp", -1, 0, false},

		{"touched 1h ago", now - h, 0, false},
		{"touched just inside the window", now - (stale - 1), 0, false},
		{"touched exactly at the window", now - stale, 0, true},
		{"touched past the window", now - (stale + h), 0, true},
		{"prompted just now", now - (stale + h), now, false},
		{"prompted just inside cooldown", now - (stale + h), now - h + 1, false},
		{"prompted exactly one hour ago", now - (stale + h), now - h, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shouldPromptProfileRefresh(c.lastTouch, c.lastPrompted, now); got != c.want {
				t.Errorf("shouldPromptProfileRefresh(%d, %d) = %v, want %v", c.lastTouch, c.lastPrompted, got, c.want)
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
			if shouldPromptProfileRefresh(got, 0, now) {
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

// Emitting records a one-hour cooldown so a five-minute feed cadence cannot
// enqueue the same task repeatedly while the previous turn is still working.
func TestMaybePromptUsesOneHourCooldown(t *testing.T) {
	tempAuthenticatedProfileHome(t)
	srv, agentID := activeProfileStateScope()
	now := time.Now().Unix()
	overdue := now - int64(profileRefreshStaleAfter/time.Second) - 3600
	if err := profilestate.Save(config.HomeDir(), srv, agentID, profilestate.State{LastRefreshUnix: overdue}); err != nil {
		t.Fatalf("seed overdue stamp: %v", err)
	}

	maybePromptProfileRefresh()
	first := profilestate.Load(config.HomeDir(), srv, agentID).LastPromptedUnix
	if first <= 0 || first > time.Now().Unix() {
		t.Fatalf("first prompt did not persist cooldown stamp: %d", first)
	}

	maybePromptProfileRefresh()
	if second := profilestate.Load(config.HomeDir(), srv, agentID).LastPromptedUnix; second != first {
		t.Fatalf("second poll inside cooldown changed prompt stamp: got %d, want %d", second, first)
	}

	_, err := profilestate.Update(config.HomeDir(), srv, agentID, func(state *profilestate.State) bool {
		state.LastPromptedUnix = time.Now().Unix() - int64(profilePromptCooldown/time.Second)
		return true
	})
	if err != nil {
		t.Fatalf("expire cooldown: %v", err)
	}
	maybePromptProfileRefresh()
	third := profilestate.Load(config.HomeDir(), srv, agentID).LastPromptedUnix
	if third <= time.Now().Unix()-int64(profilePromptCooldown/time.Second) {
		t.Fatalf("expired cooldown did not permit another prompt: %d", third)
	}

	// A completed evaluation settles the task and clears the retry stamp.
	if err := stampProfileChecked(); err != nil {
		t.Fatalf("settle profile state: %v", err)
	}
	settled := profilestate.Load(config.HomeDir(), srv, agentID)
	if settled.LastPromptedUnix != 0 {
		t.Fatalf("completion left stale prompt stamp: %d", settled.LastPromptedUnix)
	}
	touch := maxInt64(
		validProfileStamp(settled.LastRefreshUnix, now),
		validProfileStamp(settled.LastCheckedUnix, now),
	)
	if shouldPromptProfileRefresh(touch, 0, now) {
		t.Error("evaluating the profile must settle the prompt")
	}
}
