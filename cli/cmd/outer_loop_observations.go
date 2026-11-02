package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"cli.eigenflux.ai/internal/maintenance"
	"github.com/spf13/cobra"
)

type watchSetupObservationKey struct{}

type outerLoopRecorder struct {
	r            *maintenanceRecorder
	environment  maintenance.RuntimeEnvironment
	phase        string
	result       string
	code         string
	knownOutcome bool
}

func newOuterLoopRecorder(cmd *cobra.Command, component, phase string) *outerLoopRecorder {
	return newOuterLoopRecorderWithTrigger(cmd, component, phase, outerLoopTrigger(true))
}

func outerLoopTrigger(manual bool) string {
	if !manual || originFlag == publishOriginHeartbeat {
		return "auto"
	}
	return "manual"
}

func newOuterLoopRecorderWithTrigger(cmd *cobra.Command, component, phase, trigger string) *outerLoopRecorder {
	o := &outerLoopRecorder{r: newMaintenanceRecorder(activeServerName(), component, trigger, "", cmd.ErrOrStderr()), environment: maintenance.DetectRuntimeEnvironment(), result: "installed"}
	o.stage(phase)
	return o
}

func (o *outerLoopRecorder) record(result, code string, scheduler *maintenance.SchedulerReadback) {
	e := maintenance.NewEvent(o.r.attempt, o.r.component, o.r.trigger, o.phase, result)
	e.FromVersion, e.ErrorCode = version, code
	e.DurationMS = time.Since(o.r.started).Milliseconds()
	e.OuterLoop = &maintenance.OuterLoopObservation{Environment: o.environment, Scheduler: scheduler}
	o.r.Record(e)
}

func (o *outerLoopRecorder) stage(phase string) {
	o.phase = phase
	o.result, o.code, o.knownOutcome = "installed", "", false
	o.record("started", "", nil)
}

func (o *outerLoopRecorder) finish(err error) {
	result, code := o.result, o.code
	if err != nil && !o.knownOutcome {
		result = "failed"
		if o.result == "blocked" {
			result = "blocked"
		}
		if code == "" {
			code = o.phase + "_failed"
			switch {
			case errors.Is(err, os.ErrPermission):
				code = "permission_denied"
			case errors.Is(err, context.DeadlineExceeded):
				code = "timeout"
			case errors.Is(err, context.Canceled):
				code = "cancelled"
			}
		}
	}
	o.record(result, code, nil)
	if !o.r.authenticated {
		fmt.Fprintf(o.r.diagnostics, "[eigenflux:outer-loop] component=%s phase=%s result=%s error_code=%s os=%s arch=%s container=%s wsl=%s observation=local_only\n", o.r.component, o.phase, result, code, o.environment.OS, o.environment.Arch, o.environment.Container, o.environment.WSL)
	}
	o.r.Flush()
}

func watchSetupObservation(cmd *cobra.Command) *outerLoopRecorder {
	if cmd.Context() == nil {
		return nil
	}
	o, _ := cmd.Context().Value(watchSetupObservationKey{}).(*outerLoopRecorder)
	return o
}

func watchSetupStage(cmd *cobra.Command, phase string) {
	if o := watchSetupObservation(cmd); o != nil {
		o.stage(phase)
	}
}

func watchSetupOutcome(cmd *cobra.Command, result, code string) {
	if o := watchSetupObservation(cmd); o != nil {
		o.result, o.code = result, code
		o.knownOutcome = true
	}
}
