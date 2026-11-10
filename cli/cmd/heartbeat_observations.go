package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"strings"
	"time"

	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/maintenance"
	"github.com/spf13/cobra"
)

var observationToken = regexp.MustCompile(`^[A-Za-z0-9_.:/+@-]{0,128}$`)

func observationValue(v string) string {
	v = strings.TrimSpace(v)
	if observationToken.MatchString(v) {
		return v
	}
	return ""
}
func runtimeObservation(meta client.Meta, revision string) maintenance.RuntimeObservation {
	parts := strings.SplitN(meta.Host, "/", 2)
	product := runtimeProduct(meta.Host)
	if product == "terminal" {
		product = ""
	}
	productVersion := ""
	if len(parts) > 1 {
		productVersion = parts[1]
	}
	return maintenance.RuntimeObservation{Mode: observationValue(meta.Mode), Product: observationValue(product), ProductVersion: observationValue(productVersion), OS: runtime.GOOS, Arch: runtime.GOARCH, Model: observationValue(meta.Model), CLI: observationValue(version), Skills: observationValue(revision)}
}
func runtimeObservationsEnabled() bool { return os.Getenv("EIGENFLUX_RUNTIME_OBSERVATIONS") != "0" }
func observeRuntimeEnvironment(scope maintenance.Scope, meta client.Meta) {
	if !runtimeObservationsEnabled() {
		return
	}
	_ = recordMaintenanceEventAtScope(scope, runtimeEnvironmentEvent(meta, time.Now()))
}

func runtimeEnvironmentEvent(meta client.Meta, now time.Time) maintenance.Event {
	r := runtimeObservation(meta, "")
	key := sha256.Sum256([]byte(fmt.Sprintf("%s:%+v", now.In(time.FixedZone("UTC+8", 8*60*60)).Format("2006-01-02"), r)))
	e := maintenance.NewEvent(hex.EncodeToString(key[:]), "runtime", "auto", "environment", "observed")
	e.EventAt = now.UnixMilli()
	e.Runtime = &r
	return e
}

func attachCycleObservation(plan *heartbeatPlan, scope maintenance.Scope) {
	if !runtimeObservationsEnabled() || len(plan.ExecutionOrder) == 0 {
		return
	}
	current, err := maintenanceScope(scope.Server)
	if err != nil || current.AgentID != scope.AgentID || current.Home != scope.Home {
		return
	}
	env := runtimeObservation(clientMetaForServerName(scope.Server), plan.SkillRevision)
	c, err := maintenance.StartCycle(scope, plan.PlanMode, plan.ExecutionOrder, env)
	if err != nil {
		return
	}
	plan.CycleID = c.ID
}
func cycleInstructions(plan heartbeatPlan) string {
	if plan.CycleID == "" {
		return ""
	}
	prefix := plan.CLIPrefix + " heartbeat cycle-report --cycle " + plan.CycleID
	text := "\nExecution observations (client-reported; do not change business actions): after each listed stage, run " + prefix + " --stage <exact-stage-name> --result completed only when that stage completed, or --result failed only on a confirmed stage failure. An evaluated stage with no applicable work is completed. Leave unattempted or interrupted stages unreported. Before the final host response, report --stage cycle --result completed only after every listed stage completed; report --stage cycle --result failed only with a confirmed failed stage receipt. With missing evidence leave the cycle open. Before any final response, run " + plan.CLIPrefix + " heartbeat cycle-check --cycle " + plan.CycleID + " --finish. If it lists missing stages, execute those stages and check again; evaluated stages without applicable work still need their completed receipt. Preserve this cycle ID across compaction. Observation/upload failure must not repeat business work or change host notifications.\n"
	if plan.NeedCapture != nil {
		text += "Need capture receipts are internal: report need_capture completed only when its selected work is confirmed (or the query was successfully empty). A failed query/submission may record a failed stage; pending or uncertain submissions leave it unreported. An incomplete or failed Need stage, including its aggregate cycle observation, never makes the owner-facing Feed/first-check result fail, triggers a notification, or hides completed recommendations. Continue other stages; retain their independent outcomes.\n"
	}
	return text
}
func init() {
	report := &cobra.Command{Use: "cycle-report", Short: "Record a stage or completed heartbeat cycle", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		scope, err := maintenanceScope(activeServerName())
		if err != nil {
			return err
		}
		id, _ := cmd.Flags().GetString("cycle")
		stage, _ := cmd.Flags().GetString("stage")
		result, _ := cmd.Flags().GetString("result")
		code, _ := cmd.Flags().GetString("error-code")
		if err := maintenance.ReportCycleWithErrorCode(scope, id, stage, result, code); err != nil {
			return err
		}
		// Queue receipt is durable even when offline; existing uploader bounds and retries apply.
		if stage == "cycle" {
			_ = flushMaintenanceEventsAtScope(scope)
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Cycle observation recorded; upload is best effort.")
		return nil
	}}
	report.Flags().String("cycle", "", "Cycle ID from heartbeat plan")
	report.Flags().String("stage", "", "Exact planned stage or cycle")
	report.Flags().String("result", "", "completed or failed")
	report.Flags().String("error-code", "", "Optional confirmed failed-stage category: authentication_failed, permission_denied, timeout, network_error, rate_limited, server_error, host_unavailable, unknown_failure; requires a compatible server")
	heartbeatCmd.AddCommand(report)
}
