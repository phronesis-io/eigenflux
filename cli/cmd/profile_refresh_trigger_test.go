package cmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/profilestate"
	"github.com/spf13/cobra"
)

func TestActiveProfileRefreshTrigger(t *testing.T) {
	now := time.Now().Unix()
	ttl := int64(profileRefreshTriggerTTL / time.Second)
	for _, tc := range []struct {
		name  string
		state profilestate.State
		want  string
	}{
		{"no record", profilestate.State{}, "unknown"},
		{"scheduled", profilestate.State{RefreshTrigger: "scheduled", RefreshTriggerExpiresUnix: now + 60}, "scheduled"},
		{"manual", profilestate.State{RefreshTrigger: "manual", RefreshTriggerExpiresUnix: now + ttl}, "manual"},
		{"expired", profilestate.State{RefreshTrigger: "manual", RefreshTriggerExpiresUnix: now}, "unknown"},
		{"far future", profilestate.State{RefreshTrigger: "manual", RefreshTriggerExpiresUnix: now + ttl + 60}, "unknown"},
		{"no expiry", profilestate.State{RefreshTrigger: "manual"}, "unknown"},
		{"bad value", profilestate.State{RefreshTrigger: "cron", RefreshTriggerExpiresUnix: now + 60}, "unknown"},
	} {
		if got := activeProfileRefreshTrigger(tc.state, now); got != tc.want {
			t.Errorf("%s: trigger = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestSettleProfileRefreshTriggerKeepsOnlyFollowUpGrace(t *testing.T) {
	now := time.Now().Unix()
	grace := int64(profileRefreshTriggerGrace / time.Second)

	state := profilestate.State{}
	recordProfileRefreshDispatch(&state, "manual", now, profileRefreshTriggerTTL)
	settleProfileRefreshTrigger(&state, now)
	if state.RefreshTrigger != "manual" || state.RefreshTriggerExpiresUnix != now+grace {
		t.Fatalf("settled record = %+v, want manual until now+grace", state)
	}
	if got := activeProfileRefreshTrigger(state, now+grace-1); got != "manual" {
		t.Fatalf("follow-up inside grace = %q, want manual", got)
	}
	if got := activeProfileRefreshTrigger(state, now+grace); got != "unknown" {
		t.Fatalf("refresh after grace = %q, want unknown", got)
	}
	// A second completion never extends the window.
	settleProfileRefreshTrigger(&state, now+60)
	if state.RefreshTriggerExpiresUnix != now+grace {
		t.Fatalf("second completion extended the record: %+v", state)
	}

	stale := profilestate.State{RefreshTrigger: "scheduled", RefreshTriggerExpiresUnix: now - 1}
	settleProfileRefreshTrigger(&stale, now)
	if stale.RefreshTrigger != "" || stale.RefreshTriggerExpiresUnix != 0 {
		t.Fatalf("expired record was not cleared: %+v", stale)
	}
}

func TestProfileRefreshAttribution(t *testing.T) {
	for _, tc := range []struct{ source, recorded, wantSource, wantTrigger string }{
		{"cli_daily_refresh", "manual", "cli_manual_refresh", "manual"},
		{"cli_daily_refresh", "scheduled", "cli_daily_refresh", "scheduled"},
		{"cli_daily_refresh", "unknown", "cli_daily_refresh", "unknown"},
		{"cli_manual_refresh", "unknown", "cli_manual_refresh", "manual"},
		{"cli_manual_refresh", "scheduled", "cli_manual_refresh", "manual"},
	} {
		source, trigger := profileRefreshAttribution(tc.source, tc.recorded)
		if source != tc.wantSource || trigger != tc.wantTrigger {
			t.Errorf("profileRefreshAttribution(%q, %q) = %q, %q; want %q, %q", tc.source, tc.recorded, source, trigger, tc.wantSource, tc.wantTrigger)
		}
	}
}

func TestLostClaimDoesNotOverwriteNewerDispatch(t *testing.T) {
	home := t.TempDir()
	now := time.Now().Unix()
	// A forced task claimed and recorded "manual" after this slower scheduled
	// process claimed; the scheduled finish must not relabel the manual run.
	newer := profilestate.State{LastPromptedUnix: now + 1}
	recordProfileRefreshDispatch(&newer, "manual", now+1, profileRefreshTriggerTTL)
	if err := profilestate.Save(home, "srv", "agent-1", newer); err != nil {
		t.Fatal(err)
	}
	if err := finishProfileReviewClaim(home, "srv", "agent-1", now, "scheduled"); err != nil {
		t.Fatal(err)
	}
	if got := profilestate.Load(home, "srv", "agent-1"); got != newer {
		t.Fatalf("lost claim overwrote newer state: %+v, want %+v", got, newer)
	}
}

// refreshRunServer serves the requests one refresh run makes and records the
// trigger header and source each one carried.
type refreshRunServer struct {
	mu       sync.Mutex
	triggers []string
	sources  []string
}

func (s *refreshRunServer) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/agent-context":
			_, _ = w.Write([]byte(`{"code":0,"data":{"context_revision":1}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/agents/me/card/refresh-context":
			s.triggers = append(s.triggers, r.Header.Get(profileRefreshTriggerHeader))
			_, _ = w.Write([]byte(`{"code":0,"data":{"profile_version":7}}`))
		case r.Method == http.MethodPut && r.URL.Path == "/api/v2/agent-profile/fields":
			var body struct {
				Source string `json:"source"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode patch body: %v", err)
			}
			s.triggers = append(s.triggers, r.Header.Get(profileRefreshTriggerHeader))
			s.sources = append(s.sources, body.Source)
			_, _ = w.Write([]byte(`{"code":0,"data":{"profile_version":8,"changed_paths":["seeking"]}}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func newRefreshRunFixture(t *testing.T) (*refreshRunServer, string) {
	t.Helper()
	run := &refreshRunServer{}
	server := httptest.NewServer(run.handler(t))
	t.Cleanup(server.Close)
	_, serverName := runtimeTestConfig(t, server.URL, true)
	installHeartbeatTestRules(t)
	oldFormat := formatFlag
	formatFlag = "agent"
	t.Cleanup(func() { formatFlag = oldFormat })
	return run, serverName
}

func runRefreshComplete(t *testing.T) error {
	t.Helper()
	command := &cobra.Command{}
	command.Flags().Int64("expected-version", 0, "")
	if err := command.Flags().Set("expected-version", "7"); err != nil {
		t.Fatal(err)
	}
	return profileRefreshCompleteCmd.RunE(command, nil)
}

func runRefreshPatch(t *testing.T, source string) error {
	t.Helper()
	command := &cobra.Command{}
	command.SetIn(strings.NewReader(`{"seeking":["AI infra"]}`))
	command.Flags().String("file", "-", "")
	command.Flags().Int64("expected-version", 0, "")
	command.Flags().String("source", source, "")
	command.Flags().String("reason", "focus changed", "")
	if err := command.Flags().Set("expected-version", "7"); err != nil {
		t.Fatal(err)
	}
	return profilePatchCmd.RunE(command, nil)
}

func TestRefreshTaskDispatchReportsTriggerOnCompletion(t *testing.T) {
	for _, tc := range []struct {
		name    string
		force   bool
		trigger string
		source  string
	}{
		{"scheduled heartbeat", false, "scheduled", "cli_daily_refresh"},
		{"manual force", true, "manual", "cli_manual_refresh"},
	} {
		t.Run(tc.name+" no change", func(t *testing.T) {
			run, server := newRefreshRunFixture(t)
			due := time.Now().Unix() - int64(profileRefreshStaleAfter/time.Second) - 60
			if err := profilestate.Save(config.HomeDir(), server, "agent-1", profilestate.State{LastCheckedUnix: due}); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := runProfileTask(&out, nil, nil, tc.force); err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(out.String(), "EIGENFLUX PROFILE REVIEW TASK\n") {
				t.Fatalf("task was not delivered: %q", out.String())
			}
			if err := runRefreshComplete(t); err != nil {
				t.Fatal(err)
			}
			if len(run.triggers) != 1 || run.triggers[0] != tc.trigger {
				t.Fatalf("refresh-complete trigger = %q, want %q", run.triggers, tc.trigger)
			}
		})
		t.Run(tc.name+" patch", func(t *testing.T) {
			run, server := newRefreshRunFixture(t)
			due := time.Now().Unix() - int64(profileRefreshStaleAfter/time.Second) - 60
			if err := profilestate.Save(config.HomeDir(), server, "agent-1", profilestate.State{LastCheckedUnix: due}); err != nil {
				t.Fatal(err)
			}
			if err := runProfileTask(&bytes.Buffer{}, nil, nil, tc.force); err != nil {
				t.Fatal(err)
			}
			// Two writes in the same run: the follow-up keeps the attribution.
			for i := 0; i < 2; i++ {
				if err := runRefreshPatch(t, "cli_daily_refresh"); err != nil {
					t.Fatal(err)
				}
			}
			for i := range run.sources {
				if run.sources[i] != tc.source || run.triggers[i] != tc.trigger {
					t.Fatalf("patch %d sent source=%q trigger=%q, want %q/%q", i, run.sources[i], run.triggers[i], tc.source, tc.trigger)
				}
			}
			state := profilestate.Load(config.HomeDir(), server, "agent-1")
			if state.LastRefreshUnix <= 0 {
				t.Fatalf("refresh write was not stamped: %+v", state)
			}
			if remaining := state.RefreshTriggerExpiresUnix - time.Now().Unix(); remaining > int64(profileRefreshTriggerGrace/time.Second) {
				t.Fatalf("completed run kept its full attribution window: %+v", state)
			}
		})
	}
}

func TestRefreshWithoutDispatchKeepsSourceAndReportsUnknown(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state profilestate.State
	}{
		{"agent initiated", profilestate.State{}},
		{"expired manual record", profilestate.State{RefreshTrigger: "manual", RefreshTriggerExpiresUnix: time.Now().Unix() - 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run, server := newRefreshRunFixture(t)
			if err := profilestate.Save(config.HomeDir(), server, "agent-1", tc.state); err != nil {
				t.Fatal(err)
			}
			if err := runRefreshPatch(t, "cli_daily_refresh"); err != nil {
				t.Fatal(err)
			}
			if err := runRefreshComplete(t); err != nil {
				t.Fatal(err)
			}
			if len(run.sources) != 1 || run.sources[0] != "cli_daily_refresh" {
				t.Fatalf("patch source = %q, want cli_daily_refresh", run.sources)
			}
			if len(run.triggers) != 2 || run.triggers[0] != "unknown" || run.triggers[1] != "unknown" {
				t.Fatalf("triggers = %q, want unknown for patch and completion", run.triggers)
			}
		})
	}
}

func TestNonRefreshPatchCarriesNoTrigger(t *testing.T) {
	run, server := newRefreshRunFixture(t)
	state := profilestate.State{}
	recordProfileRefreshDispatch(&state, "manual", time.Now().Unix(), profileRefreshTriggerTTL)
	if err := profilestate.Save(config.HomeDir(), server, "agent-1", state); err != nil {
		t.Fatal(err)
	}
	if err := runRefreshPatch(t, "owner_request"); err != nil {
		t.Fatal(err)
	}
	if len(run.sources) != 1 || run.sources[0] != "owner_request" || run.triggers[0] != "" {
		t.Fatalf("owner patch sent source=%q trigger=%q", run.sources, run.triggers)
	}
	if got := profilestate.Load(config.HomeDir(), server, "agent-1"); got.LastRefreshUnix != 0 || got.RefreshTriggerExpiresUnix != state.RefreshTriggerExpiresUnix {
		t.Fatalf("owner patch touched refresh state: %+v", got)
	}
}

func TestPendingLineRecordsScheduledDispatch(t *testing.T) {
	tempAuthenticatedProfileHome(t)
	srv, agentID := activeProfileStateScope()
	overdue := time.Now().Unix() - int64(profileRefreshStaleAfter/time.Second) - 3600
	if err := profilestate.Save(config.HomeDir(), srv, agentID, profilestate.State{LastRefreshUnix: overdue}); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	oldWriter := profilePromptWriter
	profilePromptWriter = &stderr
	t.Cleanup(func() { profilePromptWriter = oldWriter })

	maybePromptProfileRefreshFor(srv, agentID)
	if !strings.Contains(stderr.String(), "[PENDING TASK]") {
		t.Fatalf("pending line was not emitted: %q", stderr.String())
	}
	state := profilestate.Load(config.HomeDir(), srv, agentID)
	if got := activeProfileRefreshTrigger(state, time.Now().Unix()); got != "scheduled" {
		t.Fatalf("pending line trigger = %q (%+v), want scheduled", got, state)
	}
	if remaining := state.RefreshTriggerExpiresUnix - time.Now().Unix(); remaining > int64(profilePendingLineTriggerTTL/time.Second) {
		t.Fatalf("pending line record outlives its turn window: %+v", state)
	}
	if state.LastPromptedUnix < time.Now().Unix()-5 {
		t.Fatalf("pending line delivery was not finalized: %+v", state)
	}
}

func TestFailedPendingLineRecordsNoDispatch(t *testing.T) {
	tempAuthenticatedProfileHome(t)
	srv, agentID := activeProfileStateScope()
	overdue := time.Now().Unix() - int64(profileRefreshStaleAfter/time.Second) - 3600
	if err := profilestate.Save(config.HomeDir(), srv, agentID, profilestate.State{LastRefreshUnix: overdue}); err != nil {
		t.Fatal(err)
	}
	oldWriter := profilePromptWriter
	profilePromptWriter = failingProfilePromptWriter{}
	t.Cleanup(func() { profilePromptWriter = oldWriter })

	maybePromptProfileRefreshFor(srv, agentID)
	if got := profilestate.Load(config.HomeDir(), srv, agentID).RefreshTrigger; got != "" {
		t.Fatalf("undelivered pending line recorded trigger %q", got)
	}
}
