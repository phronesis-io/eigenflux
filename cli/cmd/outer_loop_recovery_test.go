package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/dispatch"
	"cli.eigenflux.ai/internal/heartbeatmigration"
	"cli.eigenflux.ai/internal/maintenance"
	"cli.eigenflux.ai/internal/watchservice"
	"github.com/spf13/cobra"
)

func TestSchedulerReportInvocationTrigger(t *testing.T) {
	for _, origin := range []string{"", publishOriginOwner, publishOriginHeartbeat} {
		for _, failed := range []bool{false, true} {
			t.Run(origin+"/"+map[bool]string{false: "verified", true: "failed"}[failed], func(t *testing.T) {
				api := outerLoopTestAPI(t)
				_, server := runtimeTestConfig(t, api.URL, true)
				previous := originFlag
				originFlag = origin
				t.Cleanup(func() { originFlag = previous })
				in := schedulerReportInput{ObservedAt: time.Now().UnixMilli(), Inventory: heartbeatmigration.Inventory{Complete: true}}
				if failed {
					in.Complete, in.ErrorCode = false, "scheduler_readback_denied"
				}
				b, _ := json.Marshal(in)
				cmd := newSchedulerReportCommand()
				cmd.SetIn(bytes.NewReader(b))
				cmd.SetErr(&bytes.Buffer{})
				_ = cmd.Flags().Set("stdin", "true")
				_, err := captureHeartbeatStdout(t, func() error { return cmd.RunE(cmd, nil) })
				if (err != nil) != failed {
					t.Fatalf("failed=%t err=%v", failed, err)
				}
				scope, _ := maintenanceScope(server)
				state, err := maintenance.Snapshot(scope)
				if err != nil || len(state.Events) != 2 {
					t.Fatalf("events=%+v err=%v", state.Events, err)
				}
				want := "manual"
				if origin == publishOriginHeartbeat {
					want = "auto"
				}
				for _, event := range state.Events {
					if event.Trigger != want {
						t.Fatalf("origin=%q want=%s event=%+v", origin, want, event)
					}
				}
			})
		}
	}
}

func TestWatchRecoveryRecordsEarlyExits(t *testing.T) {
	for _, scenario := range []string{"linux_auto", "linux_manual", "linux_heartbeat", "disabled", "pending_auth", "post_auth_recovery"} {
		t.Run(scenario, func(t *testing.T) {
			api := outerLoopTestAPI(t)
			cfg, server := runtimeTestConfig(t, api.URL, true)
			previousOrigin := originFlag
			originFlag = ""
			t.Cleanup(func() { originFlag = previousOrigin })
			t.Setenv("EIGENFLUX_SKIP_AGENT_SETUP", "")
			cmd := &cobra.Command{}
			previousContext := context.WithValue(context.Background(), struct{ name string }{"test"}, "preserved")
			cmd.SetContext(previousContext)
			cmd.SetErr(&bytes.Buffer{})
			var result updateWatchRecovery
			wantCode, wantResult, wantTrigger := "unsupported_platform", "blocked", "auto"
			switch scenario {
			case "linux_auto", "linux_manual", "linux_heartbeat":
				manual := scenario != "linux_auto"
				if scenario == "linux_manual" {
					wantTrigger = "manual"
				}
				if scenario == "linux_heartbeat" {
					originFlag = publishOriginHeartbeat
				}
				result = recoverWatchAfterUpdateOnPlatform(cmd, cfg, manual, "linux")
				if result.Status != "pending" || result.Reason != "external_loop_unverified" || result.Action == "" || watchRecoveryError(result) == nil {
					t.Fatalf("platform behavior changed: %+v", result)
				}
			case "pending_auth":
				result = recoverWatchAfterUpdateOnPlatform(cmd, nil, true, "darwin")
				wantCode, wantResult, wantTrigger = "pending_auth", "skipped", "manual"
			case "disabled", "post_auth_recovery":
				t.Setenv("EIGENFLUX_SKIP_AGENT_SETUP", "1")
				if scenario == "post_auth_recovery" {
					if err := markWatchRecoveryPending(cfg); err != nil {
						t.Fatal(err)
					}
					pending := completePendingWatchRecovery(cmd, cfg)
					if pending == nil {
						t.Fatal("pending recovery was not resumed")
					}
					result = *pending
				} else {
					result = recoverWatchAfterUpdate(cmd, cfg, false)
				}
				wantCode, wantResult = "user_disabled", "skipped"
				if scenario == "post_auth_recovery" {
					wantTrigger = "manual"
				}
				if result.Reason != "owner_setup_disabled" {
					t.Fatalf("disabled behavior changed: %+v", result)
				}
			}
			if cmd.Context() != previousContext {
				t.Fatal("recovery leaked its recorder into the caller context")
			}
			scope, _ := maintenanceScope(server)
			state, err := maintenance.Snapshot(scope)
			if err != nil || len(state.Events) < 2 {
				t.Fatalf("missing recovery attempt: %+v %v", state, err)
			}
			first, last := state.Events[0], state.Events[len(state.Events)-1]
			if first.Phase != "preflight" || first.Result != "started" || first.AttemptID != last.AttemptID || last.ErrorCode != wantCode || last.Result != wantResult || last.OuterLoop == nil {
				t.Fatalf("wrong early recovery evidence: %+v", state.Events)
			}
			for _, event := range state.Events {
				if event.Trigger != wantTrigger {
					t.Fatalf("want trigger=%s event=%+v", wantTrigger, event)
				}
			}
		})
	}
}

func TestWatchRecoveryRecordsWorkBuddySkillsFailure(t *testing.T) {
	api := outerLoopTestAPI(t)
	cfg, server := runtimeTestConfig(t, api.URL, true)
	clientMeta.Host = "workbuddy"
	t.Setenv("EIGENFLUX_HOST", "workbuddy")
	t.Setenv("EIGENFLUX_MODE", "skill")
	t.Setenv("EIGENFLUX_SKIP_AGENT_SETUP", "")
	cmd := &cobra.Command{}
	cmd.SetErr(&bytes.Buffer{})
	result := recoverWatchAfterUpdateOnPlatform(cmd, cfg, false, "darwin")
	if result.Status != "blocked" || result.Reason != "skills_not_ready" {
		t.Fatalf("expected missing local WorkBuddy Skills: %+v", result)
	}
	scope, _ := maintenanceScope(server)
	state, err := maintenance.Snapshot(scope)
	if err != nil || len(state.Events) < 2 {
		t.Fatalf("missing WorkBuddy recovery: %+v %v", state, err)
	}
	last := state.Events[len(state.Events)-1]
	if last.Phase != "skills" || last.Result != "failed" || last.ErrorCode != "skills_failed" || last.Trigger != "auto" {
		t.Fatalf("wrong recovery failure: %+v", last)
	}
	if cmd.Context() != nil {
		t.Fatal("nil caller context was not restored")
	}
}

func TestWatchRecoveryTaskOutcomeAndFailureStages(t *testing.T) {
	for _, scenario := range []string{"skills_failed", "start_failed", "disabled", "not_ready", "running"} {
		t.Run(scenario, func(t *testing.T) {
			api := outerLoopTestAPI(t)
			_, server := runtimeTestConfig(t, api.URL, true)
			cmd := &cobra.Command{}
			cmd.SetErr(&bytes.Buffer{})
			observation := newOuterLoopRecorderWithTrigger(cmd, "watch", "preflight", "auto")
			ctx, cancel := context.WithCancel(context.WithValue(context.Background(), watchSetupObservationKey{}, observation))
			cancel()
			// A completed WorkBuddy setup must not hide a later recovery-stage failure.
			observation.result, observation.knownOutcome = "installed", true
			expected, err := watchBindingIdentity()
			if err != nil {
				t.Fatal(err)
			}
			starts := 0
			result := recoverUpdatedWatch(ctx, expected, nil, "/unused/eigenflux", updateRecoveryDeps{
				verify: func() error { return nil },
				sync: func(dispatch.Binding) error {
					if scenario == "skills_failed" {
						return &upgradeSkillsError{Reason: "skills_sync_failed", FailureReason: "skills_target_unresolved", Err: errors.New("private Skills error")}
					}
					return nil
				},
				start: func(context.Context, watchservice.NotificationTask) (watchservice.Result, error) {
					starts++
					if scenario == "start_failed" {
						return watchservice.Result{}, errors.New("private scheduler error")
					}
					if scenario == "disabled" {
						return watchservice.Result{Status: "skipped_disabled"}, nil
					}
					return watchservice.Result{Status: "process_started"}, nil
				},
				live: func(dispatch.Binding, bool) watchLiveStatus {
					return watchLiveStatus{Running: scenario == "running", Ready: scenario == "running", Commit: commit}
				},
			})
			var finishErr error
			if result.Status != "ready" {
				finishErr = errors.New("watch recovery incomplete")
			}
			observation.finish(finishErr)
			scope, _ := maintenanceScope(server)
			state, err := maintenance.Snapshot(scope)
			if err != nil || len(state.Events) < 2 {
				t.Fatalf("missing staged evidence: %+v %v", state, err)
			}
			last := state.Events[len(state.Events)-1]
			wantPhase, wantResult, wantCode := "task_start", "installed", ""
			switch scenario {
			case "skills_failed":
				wantPhase, wantResult, wantCode = "skills", "failed", "skills_failed"
			case "start_failed":
				wantResult, wantCode = "failed", "task_start_failed"
			case "disabled":
				wantResult, wantCode = "skipped", "user_disabled"
			case "not_ready":
				if result.Status != "starting" || result.RuntimeReady {
					t.Fatalf("startup was confused with readiness: %+v", result)
				}
			}
			if scenario == "skills_failed" && result.Reason != "skills_sync_failed" {
				t.Fatalf("public recovery reason changed: %+v", result)
			}
			if scenario == "skills_failed" && (last.OuterLoop == nil || last.OuterLoop.FailureReason != "skills_target_unresolved") {
				t.Fatalf("lost Skills failure detail: %+v", last)
			}
			if last.Phase != wantPhase || last.Result != wantResult || last.ErrorCode != wantCode {
				t.Fatalf("scenario=%s observation=%+v recovery=%+v", scenario, last, result)
			}
			if (scenario == "skills_failed" || scenario == "running") && starts != 0 {
				t.Fatal("started a task despite failed preflight or existing runtime")
			}
		})
	}
}
