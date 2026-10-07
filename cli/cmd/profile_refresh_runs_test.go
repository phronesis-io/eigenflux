package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/profilestate"
	"github.com/spf13/cobra"
)

// Tests never send refresh-run telemetry to a real endpoint unless they opt in
// with captureProfileRefreshRuns or call sendProfileRefreshRun directly.
func init() {
	reportProfileRefreshRun = func(string, profileRefreshRunEvent) {}
}

type capturedProfileRefreshRuns struct {
	mu     sync.Mutex
	events []profileRefreshRunEvent
}

func (c *capturedProfileRefreshRuns) all() []profileRefreshRunEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]profileRefreshRunEvent(nil), c.events...)
}

func captureProfileRefreshRuns(t *testing.T) *capturedProfileRefreshRuns {
	t.Helper()
	captured := &capturedProfileRefreshRuns{}
	previous := reportProfileRefreshRun
	reportProfileRefreshRun = func(_ string, event profileRefreshRunEvent) {
		captured.mu.Lock()
		defer captured.mu.Unlock()
		captured.events = append(captured.events, event)
	}
	t.Cleanup(func() { reportProfileRefreshRun = previous })
	return captured
}

func TestTakePendingProfileRefreshRun(t *testing.T) {
	now := time.Now().Unix()
	window := int64(profileRefreshRunLinkWindow / time.Second)
	for _, tc := range []struct {
		name        string
		state       profilestate.State
		wantLinked  bool
		wantTrigger string
	}{
		{"linked plugin task", profilestate.State{PendingRunID: "run-a", PendingRunTrigger: profileRefreshTriggerPluginTask, PendingRunDispatchedUnix: now - 60}, true, profileRefreshTriggerPluginTask},
		{"linked manual force", profilestate.State{PendingRunID: "run-a", PendingRunTrigger: profileRefreshTriggerManualForce, PendingRunDispatchedUnix: now - 60}, true, profileRefreshTriggerManualForce},
		{"no pending run", profilestate.State{}, false, profileRefreshTriggerUntracked},
		{"stale dispatch", profilestate.State{PendingRunID: "run-a", PendingRunTrigger: profileRefreshTriggerPendingLine, PendingRunDispatchedUnix: now - window - 1}, false, profileRefreshTriggerUntracked},
		{"future dispatch", profilestate.State{PendingRunID: "run-a", PendingRunTrigger: profileRefreshTriggerPendingLine, PendingRunDispatchedUnix: now + 60}, false, profileRefreshTriggerUntracked},
		{"unknown trigger", profilestate.State{PendingRunID: "run-a", PendingRunTrigger: "cron", PendingRunDispatchedUnix: now - 60}, false, profileRefreshTriggerUntracked},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := tc.state
			runID, trigger := takePendingProfileRefreshRun(&state, now)
			if trigger != tc.wantTrigger {
				t.Fatalf("trigger = %q, want %q", trigger, tc.wantTrigger)
			}
			if tc.wantLinked != (runID == "run-a") || (!tc.wantLinked && len(runID) != 32) {
				t.Fatalf("run id = %q, linked want %v", runID, tc.wantLinked)
			}
			if state.PendingRunID != "" || state.PendingRunTrigger != "" || state.PendingRunDispatchedUnix != 0 {
				t.Fatalf("pending run was not cleared: %+v", state)
			}
		})
	}
}

func TestProfileRefreshTaskReportsDispatchedRun(t *testing.T) {
	for _, tc := range []struct {
		name        string
		force       bool
		wantTrigger string
	}{
		{"scheduled", false, profileRefreshTriggerPluginTask},
		{"manual force", true, profileRefreshTriggerManualForce},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, server := profileTaskFixture(t, false)
			captured := captureProfileRefreshRuns(t)
			if err := profilestate.Save(home, server, "agent-1", profilestate.State{LastRefreshUnix: time.Now().Add(-48 * time.Hour).Unix()}); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := runProfileTask(&out, nil, nil, tc.force); err != nil {
				t.Fatal(err)
			}
			events := captured.all()
			if len(events) != 1 || events[0].Stage != "dispatched" || events[0].Trigger != tc.wantTrigger || events[0].RunID == "" {
				t.Fatalf("events = %+v", events)
			}
			state := profilestate.Load(home, server, "agent-1")
			if state.PendingRunID != events[0].RunID || state.PendingRunTrigger != tc.wantTrigger || state.PendingRunDispatchedUnix <= 0 {
				t.Fatalf("pending run not persisted: %+v", state)
			}
		})
	}
}

func TestProfileRefreshTaskNotDueReportsNothing(t *testing.T) {
	home, server := profileTaskFixture(t, false)
	captured := captureProfileRefreshRuns(t)
	if err := profilestate.Save(home, server, "agent-1", profilestate.State{LastCheckedUnix: time.Now().Unix() - 60}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runProfileTask(&out, nil, nil); err != nil {
		t.Fatal(err)
	}
	if events := captured.all(); len(events) != 0 {
		t.Fatalf("not-due task reported %+v", events)
	}
}

func TestPendingLineReportsDispatchedRun(t *testing.T) {
	tempAuthenticatedProfileHome(t)
	captured := captureProfileRefreshRuns(t)
	previousWriter := profilePromptWriter
	profilePromptWriter = io.Discard
	t.Cleanup(func() { profilePromptWriter = previousWriter })
	srv, agentID := activeProfileStateScope()
	overdue := time.Now().Add(-48 * time.Hour).Unix()
	if err := profilestate.Save(config.HomeDir(), srv, agentID, profilestate.State{LastRefreshUnix: overdue}); err != nil {
		t.Fatal(err)
	}
	maybePromptProfileRefresh()
	events := captured.all()
	if len(events) != 1 || events[0].Stage != "dispatched" || events[0].Trigger != profileRefreshTriggerPendingLine {
		t.Fatalf("events = %+v", events)
	}
	if state := profilestate.Load(config.HomeDir(), srv, agentID); state.PendingRunID != events[0].RunID {
		t.Fatalf("pending run = %+v, want %q", state, events[0].RunID)
	}
	maybePromptProfileRefresh()
	if events := captured.all(); len(events) != 1 {
		t.Fatalf("cooldown poll reported another dispatch: %+v", events)
	}
}

func TestFailedPendingLineWriteReportsNothing(t *testing.T) {
	tempAuthenticatedProfileHome(t)
	captured := captureProfileRefreshRuns(t)
	previousWriter := profilePromptWriter
	profilePromptWriter = failingProfilePromptWriter{}
	t.Cleanup(func() { profilePromptWriter = previousWriter })
	srv, agentID := activeProfileStateScope()
	if err := profilestate.Save(config.HomeDir(), srv, agentID, profilestate.State{LastRefreshUnix: time.Now().Add(-48 * time.Hour).Unix()}); err != nil {
		t.Fatal(err)
	}
	maybePromptProfileRefresh()
	if events := captured.all(); len(events) != 0 {
		t.Fatalf("undelivered line reported %+v", events)
	}
	if state := profilestate.Load(config.HomeDir(), srv, agentID); state.PendingRunID != "" || state.PendingRunTrigger != "" || state.PendingRunDispatchedUnix != 0 {
		t.Fatalf("undelivered line kept its run: %+v", state)
	}
}

// profileCompletionServer serves refresh-context and field patches for V2.
func profileCompletionServer(t *testing.T, changedPaths []string) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/agents/me/card/refresh-context":
			_, _ = w.Write([]byte(`{"code":0,"data":{"profile_version":7}}`))
		case r.Method == http.MethodPut && r.URL.Path == "/api/v2/agent-profile/fields":
			body, _ := json.Marshal(map[string]interface{}{"code": 0, "data": map[string]interface{}{"profile_version": 8, "changed_paths": changedPaths}})
			_, _ = w.Write(body)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func runProfileCommand(t *testing.T, command *cobra.Command, flags map[string]string) error {
	t.Helper()
	for name, value := range flags {
		if err := command.Flags().Set(name, value); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for name := range flags {
			flag := command.Flags().Lookup(name)
			_ = flag.Value.Set(flag.DefValue)
			flag.Changed = false
		}
	})
	return command.RunE(command, nil)
}

func silenceStdout(t *testing.T) {
	t.Helper()
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stdout
	os.Stdout = devNull
	t.Cleanup(func() {
		os.Stdout = previous
		_ = devNull.Close()
	})
}

func TestRefreshCompleteReportsUnchangedLinkedRun(t *testing.T) {
	_, server := runtimeTestConfig(t, profileCompletionServer(t, nil), true)
	captured := captureProfileRefreshRuns(t)
	silenceStdout(t)
	now := time.Now().Unix()
	if err := profilestate.Save(config.HomeDir(), server, "agent-1", profilestate.State{
		LastRefreshUnix: now - 90000, PendingRunID: "run-linked-1", PendingRunTrigger: profileRefreshTriggerPendingLine, PendingRunDispatchedUnix: now - 60,
	}); err != nil {
		t.Fatal(err)
	}
	if err := runProfileCommand(t, profileRefreshCompleteCmd, map[string]string{"expected-version": "7"}); err != nil {
		t.Fatal(err)
	}
	events := captured.all()
	want := profileRefreshRunEvent{RunID: "run-linked-1", Stage: "completed", Outcome: "unchanged", Trigger: profileRefreshTriggerPendingLine}
	if len(events) != 1 || events[0].RunID != want.RunID || events[0].Stage != want.Stage || events[0].Outcome != want.Outcome || events[0].Trigger != want.Trigger || len(events[0].ChangedPaths) != 0 {
		t.Fatalf("events = %+v, want %+v", events, want)
	}
	if state := profilestate.Load(config.HomeDir(), server, "agent-1"); state.PendingRunID != "" || state.LastCheckedUnix < now {
		t.Fatalf("completion did not settle state: %+v", state)
	}
}

func TestDailyRefreshPatchReportsChangedPaths(t *testing.T) {
	_, server := runtimeTestConfig(t, profileCompletionServer(t, []string{"seeking", "current_focus"}), true)
	captured := captureProfileRefreshRuns(t)
	silenceStdout(t)
	patch := t.TempDir() + "/patch.json"
	if err := os.WriteFile(patch, []byte(`{"seeking":["AI infra"],"current_focus":"evals"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runProfileCommand(t, profilePatchCmd, map[string]string{"file": patch, "expected-version": "7", "source": "cli_daily_refresh"}); err != nil {
		t.Fatal(err)
	}
	events := captured.all()
	if len(events) != 1 || events[0].Stage != "completed" || events[0].Outcome != "changed" ||
		events[0].Trigger != profileRefreshTriggerUntracked || len(events[0].ChangedPaths) != 2 || events[0].ChangedPaths[0] != "seeking" {
		t.Fatalf("events = %+v", events)
	}
	if state := profilestate.Load(config.HomeDir(), server, "agent-1"); state.LastRefreshUnix <= 0 {
		t.Fatalf("patch did not stamp refresh: %+v", state)
	}
}

func TestOwnerPatchDoesNotReportRefreshRun(t *testing.T) {
	runtimeTestConfig(t, profileCompletionServer(t, []string{"seeking"}), true)
	captured := captureProfileRefreshRuns(t)
	silenceStdout(t)
	patch := t.TempDir() + "/patch.json"
	if err := os.WriteFile(patch, []byte(`{"seeking":["AI infra"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runProfileCommand(t, profilePatchCmd, map[string]string{"file": patch, "expected-version": "7", "source": "owner_request"}); err != nil {
		t.Fatal(err)
	}
	if events := captured.all(); len(events) != 0 {
		t.Fatalf("non-refresh patch reported %+v", events)
	}
}

func TestSendProfileRefreshRunRoutesAndIgnoresMissingEndpoint(t *testing.T) {
	for _, tc := range []struct {
		name     string
		v2       bool
		wantPath string
		wantAuth string
	}{
		{"legacy", false, "/api/v1/agents/me/card/refresh-runs", "Bearer test-v1"},
		{"v2", true, "/api/v2/agent-profile/refresh-runs", "Bearer test-v2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var gotPath, gotAuth, gotCLI string
			var gotBody profileRefreshRunEvent
			status := http.StatusOK
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				gotPath, gotAuth, gotCLI = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("X-CLI-Ver")
				_ = json.NewDecoder(r.Body).Decode(&gotBody)
				w.WriteHeader(status)
				if status == http.StatusOK {
					_, _ = w.Write([]byte(`{"code":0,"data":{"recorded":true}}`))
				} else {
					_, _ = w.Write([]byte(`404 page not found`))
				}
			}))
			defer server.Close()
			_, serverName := runtimeTestConfig(t, server.URL, tc.v2)
			event := profileRefreshRunEvent{RunID: "run-abcdef01", Stage: "completed", Outcome: "changed", ChangedPaths: []string{"seeking"}, Trigger: profileRefreshTriggerPluginTask}
			if err := sendProfileRefreshRun(serverName, event); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			if gotPath != tc.wantPath || gotAuth != tc.wantAuth || gotCLI != "0.0.44" ||
				gotBody.RunID != event.RunID || gotBody.Outcome != "changed" || len(gotBody.ChangedPaths) != 1 {
				mu.Unlock()
				t.Fatalf("request path=%q auth=%q cli=%q body=%+v", gotPath, gotAuth, gotCLI, gotBody)
			}
			status = http.StatusNotFound
			mu.Unlock()
			if err := sendProfileRefreshRun(serverName, event); err != nil {
				t.Fatalf("old server 404 must be ignored, got %v", err)
			}
		})
	}
}

func TestSendProfileRefreshRunSkipsExpiredV2TokenWithoutRefresh(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	_, serverName := runtimeTestConfig(t, server.URL, true)
	if err := auth.SaveV2Credentials(serverName, &auth.V2Credentials{AgentID: "agent-1", AccessToken: "test-v2", RefreshToken: "test-refresh", ExpiresAt: time.Now().Add(-time.Minute).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if err := sendProfileRefreshRun(serverName, profileRefreshRunEvent{RunID: "run-abcdef01", Stage: "dispatched", Trigger: profileRefreshTriggerPluginTask}); err == nil {
		t.Fatal("expired token should skip reporting")
	}
	if requests != 0 {
		t.Fatalf("telemetry made %d network requests with an expired token", requests)
	}
}

func stubProfileRefreshRunID(t *testing.T, id string) {
	t.Helper()
	previous := newProfileRefreshRunID
	newProfileRefreshRunID = func() string { return id }
	t.Cleanup(func() { newProfileRefreshRunID = previous })
}

func TestSetPendingProfileRefreshRunReusesUnfinishedRun(t *testing.T) {
	stubProfileRefreshRunID(t, "run-b")
	now := time.Now().Unix()
	reuse := int64(profileRefreshRunReuseWindow / time.Second)
	pending := func(trigger string, at int64) profilestate.State {
		return profilestate.State{PendingRunID: "run-a", PendingRunTrigger: trigger, PendingRunDispatchedUnix: at}
	}
	for _, tc := range []struct {
		name        string
		state       profilestate.State
		trigger     string
		wantID      string
		wantTrigger string
		wantAt      int64
		wantCreated bool
	}{
		{"hourly re-prompt keeps run", pending(profileRefreshTriggerPendingLine, now-3600), profileRefreshTriggerPendingLine, "run-a", profileRefreshTriggerPendingLine, now - 3600, false},
		{"stale run is replaced", pending(profileRefreshTriggerPendingLine, now-reuse), profileRefreshTriggerPendingLine, "run-b", profileRefreshTriggerPendingLine, now, true},
		{"different automatic trigger is replaced", pending(profileRefreshTriggerPendingLine, now-60), profileRefreshTriggerPluginTask, "run-b", profileRefreshTriggerPluginTask, now, true},
		{"manual force completes unfinished plugin task", pending(profileRefreshTriggerPluginTask, now-60), profileRefreshTriggerManualForce, "run-a", profileRefreshTriggerPluginTask, now - 60, false},
		{"manual force completes unfinished pending line", pending(profileRefreshTriggerPendingLine, now-3600), profileRefreshTriggerManualForce, "run-a", profileRefreshTriggerPendingLine, now - 3600, false},
		{"manual force replaces stale automatic run", pending(profileRefreshTriggerPluginTask, now-reuse), profileRefreshTriggerManualForce, "run-b", profileRefreshTriggerManualForce, now, true},
		{"manual force after manual force starts a run", pending(profileRefreshTriggerManualForce, now-60), profileRefreshTriggerManualForce, "run-b", profileRefreshTriggerManualForce, now, true},
		{"automatic dispatch replaces a manual run", pending(profileRefreshTriggerManualForce, now-60), profileRefreshTriggerPendingLine, "run-b", profileRefreshTriggerPendingLine, now, true},
		{"no pending run", profilestate.State{}, profileRefreshTriggerPluginTask, "run-b", profileRefreshTriggerPluginTask, now, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := tc.state
			got := setPendingProfileRefreshRun(&state, tc.trigger, now)
			want := profileRefreshDispatch{RunID: tc.wantID, Trigger: tc.wantTrigger, Created: tc.wantCreated}
			if got != want || state.PendingRunID != tc.wantID || state.PendingRunTrigger != tc.wantTrigger || state.PendingRunDispatchedUnix != tc.wantAt {
				t.Fatalf("got %+v state=%+v, want %+v at=%d", got, state, want, tc.wantAt)
			}
		})
	}
}

func TestSetPendingProfileRefreshRunHandlesRandomFailure(t *testing.T) {
	stubProfileRefreshRunID(t, "")
	now := time.Now().Unix()
	reusable := profilestate.State{PendingRunID: "run-a", PendingRunTrigger: profileRefreshTriggerPendingLine, PendingRunDispatchedUnix: now - 3600}
	state := reusable
	if got := setPendingProfileRefreshRun(&state, profileRefreshTriggerPendingLine, now); got.RunID != "run-a" || got.Created || state != reusable {
		t.Fatalf("reusable run must still be reported without a new id: got %+v state %+v", got, state)
	}
	stale := profilestate.State{PendingRunID: "run-a", PendingRunTrigger: profileRefreshTriggerPendingLine, PendingRunDispatchedUnix: now - int64(profileRefreshRunReuseWindow/time.Second)}
	state = stale
	if got := setPendingProfileRefreshRun(&state, profileRefreshTriggerPendingLine, now); got != (profileRefreshDispatch{}) || state != stale {
		t.Fatalf("random failure without a reusable run must change nothing: got %+v state %+v", got, state)
	}
}

func TestPendingLineReportsReusedRunWhenRandomFails(t *testing.T) {
	tempAuthenticatedProfileHome(t)
	captured := captureProfileRefreshRuns(t)
	stubProfileRefreshRunID(t, "")
	previousWriter := profilePromptWriter
	profilePromptWriter = io.Discard
	t.Cleanup(func() { profilePromptWriter = previousWriter })
	srv, agentID := activeProfileStateScope()
	now := time.Now().Unix()
	if err := profilestate.Save(config.HomeDir(), srv, agentID, profilestate.State{
		LastRefreshUnix: now - 48*3600, LastPromptedUnix: now - 2*3600,
		PendingRunID: "run-reused-1", PendingRunTrigger: profileRefreshTriggerPendingLine, PendingRunDispatchedUnix: now - 2*3600,
	}); err != nil {
		t.Fatal(err)
	}
	maybePromptProfileRefresh()
	events := captured.all()
	if len(events) != 1 || events[0].RunID != "run-reused-1" || events[0].Trigger != profileRefreshTriggerPendingLine {
		t.Fatalf("events = %+v", events)
	}
}

func TestProfileRefreshTaskManualForceCompletesUnfinishedAutomaticRun(t *testing.T) {
	home, server := profileTaskFixture(t, false)
	captured := captureProfileRefreshRuns(t)
	now := time.Now().Unix()
	if err := profilestate.Save(home, server, "agent-1", profilestate.State{
		LastRefreshUnix: now - 48*3600, PendingRunID: "run-auto-1", PendingRunTrigger: profileRefreshTriggerPluginTask, PendingRunDispatchedUnix: now - 600,
	}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runProfileTask(&out, nil, nil, true); err != nil {
		t.Fatal(err)
	}
	events := captured.all()
	if len(events) != 1 || events[0].RunID != "run-auto-1" || events[0].Trigger != profileRefreshTriggerPluginTask {
		t.Fatalf("manual force must report the unfinished automatic run: %+v", events)
	}
	state := profilestate.Load(home, server, "agent-1")
	if state.PendingRunID != "run-auto-1" || state.PendingRunTrigger != profileRefreshTriggerPluginTask || state.PendingRunDispatchedUnix != now-600 {
		t.Fatalf("manual force replaced the automatic run: %+v", state)
	}
}

func TestUndeliveredProfileRefreshTaskClearsOnlyItsOwnRun(t *testing.T) {
	failing := profileTaskWriterFunc(func([]byte) (int, error) { return 0, errors.New("closed stdout") })
	t.Run("created run is cleared", func(t *testing.T) {
		home, server := profileTaskFixture(t, false)
		captured := captureProfileRefreshRuns(t)
		if err := profilestate.Save(home, server, "agent-1", profilestate.State{LastRefreshUnix: time.Now().Add(-48 * time.Hour).Unix()}); err != nil {
			t.Fatal(err)
		}
		if err := runProfileTask(failing, nil, nil); err == nil {
			t.Fatal("failed delivery must return an error")
		}
		if events := captured.all(); len(events) != 0 {
			t.Fatalf("undelivered task reported %+v", events)
		}
		if state := profilestate.Load(home, server, "agent-1"); state.PendingRunID != "" || state.PendingRunTrigger != "" || state.PendingRunDispatchedUnix != 0 {
			t.Fatalf("undelivered run was kept: %+v", state)
		}
	})
	t.Run("reused run is kept", func(t *testing.T) {
		home, server := profileTaskFixture(t, false)
		captureProfileRefreshRuns(t)
		now := time.Now().Unix()
		if err := profilestate.Save(home, server, "agent-1", profilestate.State{
			LastRefreshUnix: now - 48*3600, PendingRunID: "run-auto-1", PendingRunTrigger: profileRefreshTriggerPluginTask, PendingRunDispatchedUnix: now - 7200,
		}); err != nil {
			t.Fatal(err)
		}
		if err := runProfileTask(failing, nil, nil); err == nil {
			t.Fatal("failed delivery must return an error")
		}
		if state := profilestate.Load(home, server, "agent-1"); state.PendingRunID != "run-auto-1" {
			t.Fatalf("an earlier delivered run was cleared: %+v", state)
		}
	})
}

func TestDeliveredProfileRefreshTaskReportsWhenFinalizationFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("read-only directory permissions are not enforced on Windows")
	}
	home, server := profileTaskFixture(t, false)
	captured := captureProfileRefreshRuns(t)
	if err := profilestate.Save(home, server, "agent-1", profilestate.State{LastRefreshUnix: time.Now().Add(-48 * time.Hour).Unix()}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(home, 0o700) })
	writer := profileTaskWriterFunc(func(data []byte) (int, error) {
		// The task reached the agent; local bookkeeping then cannot be saved.
		if err := os.Chmod(home, 0o500); err != nil {
			return 0, err
		}
		return len(data), nil
	})
	if err := runProfileTask(writer, nil, nil); err == nil {
		t.Fatal("finalization failure must still be returned")
	}
	events := captured.all()
	if len(events) != 1 || events[0].Stage != "dispatched" || events[0].Trigger != profileRefreshTriggerPluginTask || events[0].RunID == "" {
		t.Fatalf("delivered task must be reported: %+v", events)
	}
}

func TestStoredCredentialClientHasNoCredentialHooks(t *testing.T) {
	for _, v2 := range []bool{false, true} {
		_, serverName := runtimeTestConfig(t, "http://127.0.0.1:1", v2)
		c, gotV2, err := newStoredCredentialClientForServer(serverName)
		if err != nil {
			t.Fatal(err)
		}
		if gotV2 != v2 || c.OnUnauthorized != nil || c.OnSuccess != nil {
			t.Fatalf("v2=%v: got v2=%v, hooks unauthorized=%v success=%v", v2, gotV2, c.OnUnauthorized != nil, c.OnSuccess != nil)
		}
	}
}
