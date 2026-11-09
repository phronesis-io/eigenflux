package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/dispatch"
	"cli.eigenflux.ai/internal/heartbeatmigration"
	"cli.eigenflux.ai/internal/maintenance"
	"cli.eigenflux.ai/internal/watchservice"
	"cli.eigenflux.ai/internal/workbuddy"
	"github.com/spf13/cobra"
)

func outerLoopTestAPI(t *testing.T) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/maintenance/events:batch" {
			t.Errorf("unexpected telemetry request: %s", r.URL.Path)
		}
		http.Error(w, "offline", http.StatusServiceUnavailable)
	}))
	t.Cleanup(s.Close)
	return s
}

func TestWatchSetupFailureBeforePlatformAndDiscovery(t *testing.T) {
	for _, stage := range []string{"platform", "host_discovery"} {
		t.Run(stage, func(t *testing.T) {
			api := outerLoopTestAPI(t)
			_, server := runtimeTestConfig(t, api.URL, true)
			cmd := &cobra.Command{}
			cmd.SetContext(context.Background())
			cmd.SetErr(&bytes.Buffer{})
			o := newOuterLoopRecorder(cmd, "watch", "preflight")
			cmd.SetContext(context.WithValue(cmd.Context(), watchSetupObservationKey{}, o))
			var err error
			if stage == "platform" {
				err = startWorkBuddyWatchOnPlatform(cmd, "linux")
			} else {
				err = setupWorkBuddyWatch(cmd, workBuddySetupDeps{rules: func() (string, error) { return t.TempDir(), nil }, discover: func(workbuddy.Config) (workbuddy.Config, error) { return workbuddy.Config{}, os.ErrPermission }})
			}
			if err == nil {
				t.Fatal("expected setup failure")
			}
			o.finish(err)
			scope, _ := maintenanceScope(server)
			state, err := maintenance.Snapshot(scope)
			if err != nil || len(state.Events) < 2 {
				t.Fatalf("missing early attempt: %+v %v", state, err)
			}
			first, last := state.Events[0], state.Events[len(state.Events)-1]
			if first.Phase != "preflight" || first.Result != "started" || first.AttemptID != last.AttemptID {
				t.Fatalf("attempt not started before validation: %+v", state.Events)
			}
			wantPhase, wantCode := "preflight", "unsupported_platform"
			if stage == "host_discovery" {
				wantPhase, wantCode = "host_discovery", "permission_denied"
			}
			if last.Phase != wantPhase || last.ErrorCode != wantCode || last.OuterLoop == nil || o.r.Status().Status != "queued" {
				t.Fatalf("incorrect failure evidence: %+v", last)
			}
		})
	}
}

func TestWatchStartCommandRecordsBeforeHostRejection(t *testing.T) {
	api := outerLoopTestAPI(t)
	_, server := runtimeTestConfig(t, api.URL, true)
	cmd := newWatchStartCommand()
	cmd.SetErr(&bytes.Buffer{})
	_ = cmd.Flags().Set("host", "invalid")
	if err := cmd.RunE(cmd, nil); err == nil {
		t.Fatal("unsupported host accepted")
	}
	scope, _ := maintenanceScope(server)
	state, err := maintenance.Snapshot(scope)
	if err != nil || len(state.Events) != 2 || state.Events[1].ErrorCode != "unsupported_host" {
		t.Fatalf("%+v %v", state, err)
	}
}

type outerLoopClosedWriter struct{}

func (outerLoopClosedWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestWatchSetupTerminalOutcomeSurvivesOutputFailure(t *testing.T) {
	for _, outputFails := range []bool{false, true} {
		t.Run(fmt.Sprint(outputFails), func(t *testing.T) {
			api := outerLoopTestAPI(t)
			w := newCommissionWatch(t, api.URL, "commission_order")
			if err := os.Remove(dispatch.BindingPath(w.home, w.server.Name)); err != nil {
				t.Fatal(err)
			}
			cmd := &cobra.Command{}
			cmd.SetContext(context.Background())
			cmd.SetOut(&bytes.Buffer{})
			if outputFails {
				cmd.SetOut(outerLoopClosedWriter{})
			}
			cmd.SetErr(&bytes.Buffer{})
			o := newOuterLoopRecorder(cmd, "watch", "preflight")
			cmd.SetContext(context.WithValue(cmd.Context(), watchSetupObservationKey{}, o))
			err := setupWorkBuddyWatch(cmd, workBuddySetupDeps{
				rules:    func() (string, error) { return t.TempDir(), nil },
				discover: func(c workbuddy.Config) (workbuddy.Config, error) { c.Node = os.Args[0]; return c, nil },
				probe:    func(context.Context, workbuddy.Config, string) error { return nil },
				start: func(_ context.Context, task watchservice.NotificationTask) (watchservice.Result, error) {
					if task.PrepareOnly {
						return watchservice.Result{Status: "prepared"}, nil
					}
					if outputFails {
						return watchservice.Result{Status: "process_started"}, nil
					}
					return watchservice.Result{Status: "skipped_disabled"}, nil
				},
			})
			if outputFails {
				if !errors.Is(err, io.ErrClosedPipe) {
					t.Fatalf("original output error changed: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			o.finish(err)
			state, err := maintenance.Snapshot(o.r.scope)
			if err != nil || len(state.Events) == 0 {
				t.Fatalf("%+v %v", state, err)
			}
			last := state.Events[len(state.Events)-1]
			wantResult, wantCode := "skipped", "user_disabled"
			if outputFails {
				wantResult, wantCode = "installed", ""
			}
			if last.Result != wantResult || last.ErrorCode != wantCode {
				t.Fatalf("disabled task claimed installed: %+v", last)
			}

		})
	}
}

func TestOuterLoopObservationKeepsOriginalErrorAndIdentity(t *testing.T) {
	api := outerLoopTestAPI(t)
	_, server := runtimeTestConfig(t, api.URL, true)
	cmd := &cobra.Command{}
	cmd.SetErr(&bytes.Buffer{})
	o := newOuterLoopRecorder(cmd, "watch", "preflight")
	creds, _ := auth.LoadV2Credentials(server)
	creds.AgentID = "changed"
	_ = auth.SaveV2Credentials(server, creds)
	o.stage("identity")
	o.finish(errors.New("raw error /private/path secret"))
	state, err := maintenance.Snapshot(o.r.scope)
	if err != nil || len(state.Events) != 1 {
		t.Fatalf("identity switch recorded later events: %+v %v", state, err)
	}
	next, _ := maintenanceScope(server)
	other, _ := maintenance.Snapshot(next)
	if len(other.Events) != 0 {
		t.Fatal("observation moved to another identity")
	}
}

func TestSchedulerReportFreshnessOwnershipStateAndPrivacy(t *testing.T) {
	for _, scenario := range []string{"enabled", "paused", "disabled", "unknown", "missing", "partial", "stale", "future", "duplicate", "ambiguous", "empty_prompt", "empty_schedule", "api_failed", "private_field"} {
		t.Run(scenario, func(t *testing.T) {
			api := outerLoopTestAPI(t)
			_, server := runtimeTestConfig(t, api.URL, true)
			oldFormat := formatFlag
			formatFlag = "json"
			t.Cleanup(func() { formatFlag = oldFormat })
			task := heartbeatmigration.Task{ID: "private-task-id", Name: "private-task-name", Prompt: "private execution prompt", Owner: "eigenflux", Purpose: "heartbeat", Home: config.HomeDir(), Server: server, Schedule: "private schedule", Status: "ACTIVE"}
			in := schedulerReportInput{ObservedAt: time.Now().UnixMilli(), Inventory: heartbeatmigration.Inventory{Complete: true, Tasks: []heartbeatmigration.Task{task}}}
			switch scenario {
			case "paused":
				in.Tasks[0].Status = "PAUSED"
			case "disabled":
				in.Tasks[0].Status = "DISABLED"
			case "unknown":
				in.Tasks[0].Status = "RUNNING"
			case "missing":
				in.Tasks = nil
			case "partial":
				in.Complete = false
			case "stale":
				in.ObservedAt = time.Now().Add(-6 * time.Minute).UnixMilli()
			case "future":
				in.ObservedAt = time.Now().Add(time.Minute).UnixMilli()
			case "duplicate":
				in.Tasks = append(in.Tasks, task)
			case "ambiguous":
				task.ID = "different"
				in.Tasks = append(in.Tasks, task)
			case "empty_prompt":
				in.Tasks[0].Prompt = ""
			case "empty_schedule":
				in.Tasks[0].Schedule = " "
			case "api_failed":
				in.Inventory = heartbeatmigration.Inventory{}
				in.ErrorCode = "scheduler_api_unavailable"
			}
			b, _ := json.Marshal(in)
			if scenario == "private_field" {
				b = append(b[:len(b)-1], []byte(`,"environment":{"raw":"private env"}}`)...)
			}
			cmd := newSchedulerReportCommand()
			cmd.SetIn(bytes.NewReader(b))
			cmd.SetErr(&bytes.Buffer{})
			_ = cmd.Flags().Set("stdin", "true")
			_, err := captureHeartbeatStdout(t, func() error { return cmd.RunE(cmd, nil) })
			valid := scenario == "enabled" || scenario == "paused" || scenario == "disabled" || scenario == "unknown" || scenario == "missing"
			if (err == nil) != valid {
				t.Fatalf("valid=%t err=%v", valid, err)
			}
			scope, _ := maintenanceScope(server)
			state, stateErr := maintenance.Snapshot(scope)
			if stateErr != nil || len(state.Events) != 2 {
				t.Fatalf("missing durable readback result: %+v %v", state, stateErr)
			}
			last := state.Events[len(state.Events)-1]
			if valid {
				if last.Result != "verified" || last.OuterLoop.Scheduler.State != scenario {
					t.Fatalf("wrong receipt: %+v", last)
				}
			} else if last.Result != "failed" || last.OuterLoop.Scheduler != nil {
				t.Fatalf("invalid readback promoted: %+v", last)
			}
			payload, _ := json.Marshal(state.Events)
			for _, private := range []string{task.ID, task.Name, task.Prompt, task.Schedule, config.HomeDir(), "private env"} {
				if private != "" && strings.Contains(string(payload), private) {
					t.Fatalf("private scheduler value uploaded: %s", private)
				}
			}
		})
	}
}

func TestOuterLoopSkillsFailureDetailDoesNotLeakCause(t *testing.T) {
	api := outerLoopTestAPI(t)
	runtimeTestConfig(t, api.URL, true)
	cmd := &cobra.Command{}
	cmd.SetErr(&bytes.Buffer{})
	recorder := newOuterLoopRecorder(cmd, "watch", "skills")
	recorder.finish(&upgradeSkillsError{Reason: "skills_signature_failed", Err: errors.New("/private/path token=secret")})
	state, err := maintenance.Snapshot(recorder.r.scope)
	if err != nil || len(state.Events) < 2 {
		t.Fatalf("missing events: %+v %v", state, err)
	}
	last := state.Events[len(state.Events)-1]
	if last.ErrorCode != "skills_failed" || last.OuterLoop == nil || last.OuterLoop.FailureReason != "skills_signature_failed" {
		t.Fatalf("lost classification: %+v", last)
	}
	raw, err := json.Marshal(state.Events)
	if err != nil || bytes.Contains(raw, []byte("/private/path")) || bytes.Contains(raw, []byte("secret")) {
		t.Fatalf("private error in telemetry: %s %v", raw, err)
	}
}
