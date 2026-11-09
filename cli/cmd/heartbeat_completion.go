package cmd

import (
	"encoding/json"
	"fmt"

	"cli.eigenflux.ai/internal/maintenance"
	"github.com/spf13/cobra"
)

func cycleReadInstructions(p heartbeatPlan) string {
	if p.CycleID == "" {
		return ""
	}
	return fmt.Sprintf("\nRULE DELIVERY GATE: Run %s heartbeat rules --cycle %s --page 1. Return each page body and its receipt verbatim through the tool's model-facing output. Acknowledge that page only after its complete body is visible, then follow the next page command. Keep each tool result separate; recover truncated content before acknowledging. Finish every page before business stages.\n", p.CLIPrefix, p.CycleID)
}

func init() {
	rules := &cobra.Command{Use: "rules", Short: "Read and acknowledge one bounded page of this cycle's rules", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		scope, err := maintenanceScope(activeServerName())
		if err != nil {
			return err
		}
		id, _ := cmd.Flags().GetString("cycle")
		page, _ := cmd.Flags().GetInt("page")
		ack, _ := cmd.Flags().GetString("ack")
		if ack != "" {
			if err := maintenance.AcknowledgeCycleRule(scope, id, page, ack); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Rule page acknowledged.")
			return nil
		}
		r, body, err := maintenance.ReadCycleRule(scope, id, page)
		if err != nil {
			return err
		}
		c, err := maintenance.ReadCycle(scope, id)
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "RULE PAGE %d/%d — %s\n%s\nEND RULE PAGE %d\nAfter reading this complete page, acknowledge: heartbeat rules --cycle %s --page %d --ack %s\n", page, len(c.Rules), r.Path, body, page, id, page, r.Digest)
		if page < len(c.Rules) {
			fmt.Fprintf(cmd.OutOrStdout(), "Then read: heartbeat rules --cycle %s --page %d\n", id, page+1)
		}
		return nil
	}}
	rules.Flags().String("cycle", "", "Exact cycle ID from the plan")
	rules.Flags().Int("page", 1, "One-based rule page")
	rules.Flags().String("ack", "", "Receipt printed after the visible page body")
	heartbeatCmd.AddCommand(rules)
	check := &cobra.Command{Use: "cycle-check", Short: "Reject missing rules or stages before ending a heartbeat", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		scope, err := maintenanceScope(activeServerName())
		if err != nil {
			return err
		}
		id, _ := cmd.Flags().GetString("cycle")
		finish, _ := cmd.Flags().GetBool("finish")
		c, err := maintenance.ReadCycle(scope, id)
		if err != nil {
			return err
		}
		rules, stages := maintenance.CycleMissing(c)
		status := "incomplete"
		if len(rules) == 0 && len(stages) == 0 {
			status = "ready"
		}
		if c.Result != "" {
			status = c.Result
		}
		if finish && status == "ready" {
			if err := maintenance.ReportCycle(scope, id, "cycle", "completed"); err != nil {
				return err
			}
			status = "completed"
			_ = flushMaintenanceEventsAtScope(scope)
		}
		if err := json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"cycle_id": id, "status": status, "pending_rule_pages": rules, "missing_stages": stages}); err != nil {
			return err
		}
		if status == "incomplete" {
			_ = maintenance.ObserveCycle(scope, id, "incomplete")
			return fmt.Errorf("heartbeat incomplete: read pending rule pages and execute missing stages before ending")
		}
		return nil
	}}
	check.Flags().String("cycle", "", "Exact cycle ID from the plan")
	check.Flags().Bool("finish", false, "Atomically record completion after all checks pass")
	heartbeatCmd.AddCommand(check)
}
