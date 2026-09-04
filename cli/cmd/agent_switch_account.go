package cmd

import (
	"encoding/json"
	"fmt"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/output"
	"github.com/spf13/cobra"
)

var agentV2SwitchAccountCmd = &cobra.Command{
	Use:   "switch-account",
	Short: "Switch this Agent Home to another EigenFlux account",
	Long: `Create a short-lived Console link for replacing the account used by this
Agent Home. Selecting the current account confirms it without changing credentials.
A different target account must be verified in the Console. The current CLI login
remains active until a new target account has completed onboarding.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		v2Client, server, err := newV2ClientForServer(serverFlag, true)
		if err != nil {
			return err
		}
		credentials, err := auth.LoadV2Credentials(server.Name)
		if err != nil {
			return err
		}
		browserNonce, err := newBrowserNonce()
		if err != nil {
			return err
		}
		response, err := v2Client.Post("/console/handoffs", map[string]interface{}{
			"browser_nonce": browserNonce, "client_capabilities": cliAccountSwitchCapabilities(),
		})
		if err != nil {
			return err
		}
		var handoff struct {
			URL       string `json:"handoff_url"`
			ExpiresAt int64  `json:"expires_at"`
		}
		if json.Unmarshal(response.Data, &handoff) != nil || validateConsoleHandoffURL(handoff.URL) != nil {
			return fmt.Errorf("invalid Console V2 account-switch handoff response")
		}
		output.PrintData(map[string]interface{}{
			"agent_id": credentials.AgentID, "console_url": handoff.URL,
			"handoff_expires_at": handoff.ExpiresAt,
		}, resolveFormat())
		return nil
	},
}

func init() {
	agentV2Cmd.AddCommand(agentV2SwitchAccountCmd)
}

func cliAccountSwitchCapabilities() []string {
	return []string{"account_switch_v1"}
}
