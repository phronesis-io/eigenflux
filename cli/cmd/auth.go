package cmd

import (
	"encoding/json"
	"fmt"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/output"
	"github.com/spf13/cobra"
)

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Authentication commands",
	Long:  "Log in to an EigenFlux server and manage credentials.",
}

var authLoginCmd = &cobra.Command{
	Use:   "login",
	Short: "Log in with email",
	Long: `Start authentication with your email address.

Examples:
  eigenflux auth login --email user@example.com
  eigenflux auth login --email user@example.com --server staging`,
	RunE: func(cmd *cobra.Command, args []string) error {
		email, _ := cmd.Flags().GetString("email")
		if email == "" {
			return fmt.Errorf("--email is required")
		}
		c := newClientNoAuth()
		resp, err := c.Post("/auth/login", map[string]interface{}{
			"login_method": "email",
			"email":        email,
		})
		if err != nil {
			return err
		}
		if resp.Code != 0 {
			return fmt.Errorf("login failed: %s", resp.Msg)
		}
		var data struct {
			VerificationRequired bool   `json:"verification_required"`
			ChallengeID          string `json:"challenge_id"`
			AgentID              string `json:"agent_id"`
			AccessToken          string `json:"access_token"`
			ExpiresAt            int64  `json:"expires_at"`
		}
		json.Unmarshal(resp.Data, &data)
		if data.VerificationRequired {
			output.PrintMessage("OTP verification required. Check your email and run:")
			output.PrintMessage("  eigenflux auth verify --challenge-id %s --code <OTP_CODE>", data.ChallengeID)
			output.PrintData(json.RawMessage(resp.Data), resolveFormat())
			return nil
		}
		cfg, _ := config.Load()
		srv, _ := cfg.GetActive(serverFlag)
		err = auth.SaveCredentials(srv.Name, &auth.Credentials{
			AccessToken: data.AccessToken,
			Email:       email,
			ExpiresAt:   data.ExpiresAt,
		})
		if err != nil {
			return fmt.Errorf("save credentials: %w", err)
		}
		output.PrintMessage("Logged in successfully to server %q", srv.Name)
		output.PrintData(json.RawMessage(resp.Data), resolveFormat())
		return nil
	},
}

var authVerifyCmd = &cobra.Command{
	Use:   "verify",
	Short: "Verify OTP code",
	Long: `Complete login by verifying the OTP code sent to your email.

Examples:
  eigenflux auth verify --challenge-id ch_xxx --code 123456`,
	RunE: func(cmd *cobra.Command, args []string) error {
		challengeID, _ := cmd.Flags().GetString("challenge-id")
		code, _ := cmd.Flags().GetString("code")
		if challengeID == "" || code == "" {
			return fmt.Errorf("--challenge-id and --code are required")
		}
		c := newClientNoAuth()
		resp, err := c.Post("/auth/login/verify", map[string]interface{}{
			"login_method": "email",
			"challenge_id": challengeID,
			"code":         code,
		})
		if err != nil {
			return err
		}
		if resp.Code != 0 {
			return fmt.Errorf("verification failed: %s", resp.Msg)
		}
		var data struct {
			AgentID     string `json:"agent_id"`
			AccessToken string `json:"access_token"`
			ExpiresAt   int64  `json:"expires_at"`
		}
		json.Unmarshal(resp.Data, &data)
		cfg, _ := config.Load()
		srv, _ := cfg.GetActive(serverFlag)
		err = auth.SaveCredentials(srv.Name, &auth.Credentials{
			AccessToken: data.AccessToken,
			ExpiresAt:   data.ExpiresAt,
		})
		if err != nil {
			return fmt.Errorf("save credentials: %w", err)
		}
		output.PrintMessage("Logged in successfully to server %q", srv.Name)
		output.PrintData(json.RawMessage(resp.Data), resolveFormat())
		return nil
	},
}

func init() {
	authLoginCmd.Flags().String("email", "", "email address to log in with (required)")
	authVerifyCmd.Flags().String("challenge-id", "", "challenge ID from login response (required)")
	authVerifyCmd.Flags().String("code", "", "OTP code from email (required)")
	authCmd.AddCommand(authLoginCmd, authVerifyCmd)
	rootCmd.AddCommand(authCmd)
}
