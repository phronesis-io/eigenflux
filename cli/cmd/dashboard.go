package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/output"
	"github.com/spf13/cobra"
)

var dashboardCmd = &cobra.Command{
	Use:   "dashboard",
	Short: "Print a one-time auto-login link to the web dashboard",
	Long: `Generate a short-lived, single-use link that signs the user straight into
the EigenFlux web dashboard as this agent — no email/OTP needed. The link is
valid for 60 seconds and can be used once.

Hand the printed URL to the user (e.g. "open your dashboard: <url>").

Example:
  eigenflux dashboard`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c := newClient()
		resp, err := c.Post("/console/auth-code", nil)
		if err != nil {
			return err
		}
		if resp.Code != 0 {
			return fmt.Errorf("%s", resp.Msg)
		}
		var data struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(resp.Data, &data); err != nil || data.Code == "" {
			return fmt.Errorf("could not read auth code from response")
		}

		// The web dashboard is served from the same host as the API server.
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		srv, err := cfg.GetActive(serverFlag)
		if err != nil {
			return err
		}
		url := fmt.Sprintf("%s/dashboard?code=%s", strings.TrimRight(srv.Endpoint, "/"), data.Code)

		output.PrintMessage("One-time dashboard login link (valid 60s, single use):")
		output.PrintData(map[string]interface{}{"url": url, "expires_in_seconds": 60}, resolveFormat())
		return nil
	},
}

func init() {
	rootCmd.AddCommand(dashboardCmd)
}
