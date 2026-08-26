package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/output"
	"github.com/spf13/cobra"
)

const dashboardLinkTTL = 15 * time.Minute

var dashboardCmd = &cobra.Command{
	Use:   "dashboard",
	Short: "Print a one-time auto-login link to the web dashboard",
	Long: `Generate a short-lived, single-use link that signs the user straight into
the EigenFlux web dashboard as this agent — no email/OTP needed. The link is
valid for 15 minutes and can be used once.

Hand the printed URL to the user (e.g. "open your dashboard: <url>").

Example:
	  eigenflux dashboard`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		srv, err := cfg.GetActive(serverFlag)
		if err != nil {
			return err
		}
		if _, v2Err := auth.LoadV2Credentials(srv.Name); v2Err == nil {
			v2Client, _, err := newV2ClientForServer(srv.Name, true)
			if err != nil {
				if !consoleV2Unavailable(err) {
					return err
				}
			} else {
				browserNonce, err := newBrowserNonce()
				if err != nil {
					return err
				}
				response, postErr := v2Client.Post("/console/handoffs", map[string]interface{}{"browser_nonce": browserNonce})
				if postErr == nil {
					var data struct {
						URL       string `json:"handoff_url"`
						ExpiresAt int64  `json:"expires_at"`
					}
					if json.Unmarshal(response.Data, &data) != nil || data.URL == "" {
						return fmt.Errorf("could not read Console V2 handoff from response")
					}
					output.PrintMessage("One-time Console V2 link (valid 15 min, single use):")
					output.PrintData(map[string]interface{}{"url": data.URL, "expires_at": data.ExpiresAt}, resolveFormat())
					return nil
				}
				if !consoleV2Unavailable(postErr) {
					return postErr
				}
			}
		}
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
		url := fmt.Sprintf("%s/dashboard?code=%s", strings.TrimRight(srv.Endpoint, "/"), data.Code)

		output.PrintMessage("One-time dashboard login link (valid 15 min, single use):")
		output.PrintData(map[string]interface{}{"url": url, "expires_in_seconds": int(dashboardLinkTTL.Seconds())}, resolveFormat())
		return nil
	},
}

func consoleV2Unavailable(err error) bool {
	var apiErr *client.APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound
}

func init() {
	rootCmd.AddCommand(dashboardCmd)
}
