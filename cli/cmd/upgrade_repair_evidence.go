package cmd

import (
	"encoding/json"
	"strings"

	"cli.eigenflux.ai/internal/maintenance"
)

// A read-only cycle-check cannot turn a pre-trigger foreground completion into
// unattended acceptance. Continuations retain the original canonical trigger.
func upgradeRepairTriggerAt(scope maintenance.Scope, thread, turn string, c maintenance.Cycle, transcripts ...string) (int64, error) {
	if c.Result != "completed" || c.Mode != "full" {
		return 0, nil
	}
	currentAttempt := heartbeatHostAttempt(thread, turn)
	marker, ok := readHeartbeatHostMarker(scope, thread, currentAttempt)
	if !ok || marker.Cycle != "" && marker.Cycle != c.ID {
		return 0, nil
	}
	attempt := marker.OriginAttempt
	if attempt == "" {
		attempt = currentAttempt
	}
	at, err := maintenance.LifecycleObservedAt(scope, attempt, "trigger", "observed")
	if err != nil {
		return 0, err
	}
	if at == 0 || c.StartedAt <= at {
		return 0, nil
	}
	if len(transcripts) != 1 || transcripts[0] == "" {
		return 0, nil
	}
	planCycle := ""
	err = visitHeartbeatCommands(transcripts[0], func(candidate string) bool {
		return heartbeatHostAttempt(thread, candidate) == attempt
	}, func(item heartbeatCommandItem) {
		if item.Status != "completed" || strings.Contains(string(item.Command), "cycle-check") {
			return
		}
		if id := upgradeRepairPlanCycle(item.Output); id != "" {
			planCycle = id
		}
	})
	if err != nil {
		return 0, err
	}
	if planCycle != c.ID {
		return 0, nil
	}
	return at, nil
}

// Plan output establishes creation in the origin turn. A cycle-check receipt,
// quoted header or malformed/mixed-cycle plan cannot establish that source.
func upgradeRepairPlanCycle(output string) string {
	if strings.HasPrefix(output, "{") {
		var plan struct {
			Schema string `json:"schema_version"`
			Prompt string `json:"agent_prompt"`
		}
		if json.Unmarshal([]byte(output), &plan) != nil || plan.Schema != "eigenflux_heartbeat_plan.v1" {
			return ""
		}
		output = plan.Prompt
	}
	if !strings.HasPrefix(output, "EIGENFLUX HEARTBEAT PLAN\n") {
		return ""
	}
	id := ""
	for _, match := range cycleInPlan.FindAllStringSubmatch(output, -1) {
		if id != "" && id != match[1] {
			return ""
		}
		id = match[1]
	}
	return id
}
