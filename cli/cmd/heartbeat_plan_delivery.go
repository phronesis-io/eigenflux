package cmd

import (
	"encoding/json"
	"fmt"
	"io"

	"cli.eigenflux.ai/internal/maintenance"
)

// Successful plan delivery is separate from the first executed stage. A failed
// observation never repeats business work or turns a delivered plan into a failure.
func deliverHeartbeatPlan(out, diagnostics io.Writer, format string, plan heartbeatPlan, scope maintenance.Scope) error {
	var err error
	if format == "agent" {
		_, err = io.WriteString(out, plan.AgentPrompt)
	} else {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		err = enc.Encode(plan)
	}
	if err != nil {
		return err
	}
	if plan.CycleID != "" {
		if err := maintenance.ObserveCycle(scope, plan.CycleID, "plan_created"); err != nil {
			fmt.Fprintln(diagnostics, "[eigenflux:maintenance] component=runtime phase=cycle error_code=observation_unavailable")
		}
	}
	return nil
}
