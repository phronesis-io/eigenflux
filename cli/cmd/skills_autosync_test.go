package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/skills"
)

func TestClassifyAutoSync(t *testing.T) {
	cases := []struct {
		name string
		res  *skills.SyncResult
		err  error
		want string
	}{
		{"error from err", nil, errors.New("boom"), "error"},
		{"error on nil result", nil, nil, "error"},
		{"offline", &skills.SyncResult{NoNetwork: true}, nil, "offline"},
		{"offline wins over source", &skills.SyncResult{NoNetwork: true, Source: "local"}, nil, "offline"},
		{"nochange (local source)", &skills.SyncResult{Source: "local"}, nil, "nochange"},
		{"ok (real update)", &skills.SyncResult{Source: "cli/latest"}, nil, "ok"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyAutoSync(c.res, c.err); got != c.want {
				t.Fatalf("classifyAutoSync = %q, want %q", got, c.want)
			}
		})
	}
}

// tempHome points config.HomeDir() at a throwaway dir and returns the resolved
// home (EIGENFLUX_HOME gets a ".eigenflux" suffix appended).
func tempHome(t *testing.T) string {
	t.Helper()
	t.Setenv("EIGENFLUX_HOME", t.TempDir())
	return config.HomeDir()
}

func TestMaybeSyncSkills_DisabledByConfig(t *testing.T) {
	home := tempHome(t)
	maybeSyncSkills(&config.Config{KV: map[string]string{autoSkillSyncKey: "false"}})
	if _, err := os.Stat(filepath.Join(home, skills.AutoSyncStateFile)); !os.IsNotExist(err) {
		t.Fatalf("disabled: expected no state file to be written, stat err=%v", err)
	}
}

func TestMaybeSyncSkills_ThrottledNotDue(t *testing.T) {
	home := tempHome(t)
	// A fresh attempt timestamp => not due => must NOT re-attempt (and must not
	// touch the network).
	fresh := skills.AutoSyncState{LastAttemptUnix: time.Now().Unix(), LastResult: "ok"}
	if err := skills.SaveAutoSyncState(home, fresh); err != nil {
		t.Fatal(err)
	}
	// Point the CDN at a dead addr: if the throttle leaks, classifyAutoSync
	// would overwrite LastResult with offline/error and we'd catch it.
	t.Setenv("EIGENFLUX_CDN_URL", "http://127.0.0.1:1")

	maybeSyncSkills(&config.Config{})

	got := skills.LoadAutoSyncState(home)
	if got.LastAttemptUnix != fresh.LastAttemptUnix || got.LastResult != "ok" {
		t.Fatalf("not-due: state should be untouched, got %+v", got)
	}
}

func TestMaybeSyncSkills_DueOfflineStampsAttempt(t *testing.T) {
	home := tempHome(t)
	t.Setenv("EIGENFLUX_CDN_URL", "http://127.0.0.1:1")                // connection refused (fast)
	t.Setenv("EIGENFLUX_SKILLS_DIR", filepath.Join(t.TempDir(), "sk")) // keep the real dir untouched

	// No prior state => due. Offline sync must not panic, must stamp the
	// attempt (so it won't re-punch the gate next heartbeat), and must not
	// record success.
	maybeSyncSkills(&config.Config{})

	got := skills.LoadAutoSyncState(home)
	if got.LastAttemptUnix == 0 {
		t.Fatal("offline+due: expected the attempt to be stamped in state")
	}
	if got.LastResult == "ok" {
		t.Fatalf("offline+due: result must not be ok, got %q", got.LastResult)
	}
}
