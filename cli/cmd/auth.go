package cmd

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/cache"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/output"
	"github.com/spf13/cobra"
)

var refRe = regexp.MustCompile(`^EF-[0-9A-Za-z]{8}$`)

// reportInstallRef reports a referral code to the public install-attribution
// endpoint after a successful login, tying this install back to the ad campaign
// that minted it on the /install page. Best-effort: never blocks or fails login,
// and a malformed/empty ref is silently ignored.
func reportInstallRef(ref, agentID, email string) {
	ref = strings.TrimSpace(ref)
	if !refRe.MatchString(ref) {
		return
	}
	c := newClientNoAuth()
	_, _ = c.Post("/install/report", map[string]interface{}{
		"ref": ref,
		"metadata": map[string]interface{}{
			"via":      "cli",
			"agent_id": agentID,
			"email":    email,
		},
	})
}

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
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		srv, err := cfg.GetActive(serverFlag)
		if err != nil {
			return fmt.Errorf("%w", err)
		}
		// Guardrail: identity = home. If this home already holds credentials for a
		// DIFFERENT (or unknown) email, logging in would silently overwrite that
		// agent's identity — the classic mistake of a second agent on the same
		// machine reusing the default home. Refuse with the remedy in the error;
		// same-email re-login passes, --force overrides intentionally.
		if force, _ := cmd.Flags().GetBool("force"); !force {
			if creds, _ := auth.LoadCredentials(srv.Name); creds != nil && creds.AccessToken != "" && !strings.EqualFold(creds.Email, email) {
				owner := creds.Email
				if owner == "" {
					owner = "an unknown email"
				}
				return fmt.Errorf(`this EigenFlux home (%s) already holds an identity for %s on server %q.
Logging in as %s here would overwrite that agent's credentials.

If you are a different agent on this machine, use your own home instead:
  EIGENFLUX_HOME=<your-own-dir> eigenflux auth login --email %s

To intentionally replace the existing identity, re-run with --force.`,
					config.HomeDir(), owner, srv.Name, email, email)
			}
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
		if err := json.Unmarshal(resp.Data, &data); err != nil {
			return fmt.Errorf("parse login response: %w", err)
		}
		if data.VerificationRequired {
			output.PrintMessage("OTP verification required. Check your email and run:")
			output.PrintMessage("  eigenflux auth verify --challenge-id %s --code <OTP_CODE>", data.ChallengeID)
			output.PrintData(json.RawMessage(resp.Data), resolveFormat())
			return nil
		}
		err = auth.SaveCredentials(srv.Name, &auth.Credentials{
			AccessToken: data.AccessToken,
			Email:       email,
			AgentID:     data.AgentID,
			ExpiresAt:   data.ExpiresAt,
		})
		if err != nil {
			return fmt.Errorf("save credentials: %w", err)
		}
		output.PrintMessage("Logged in successfully to server %q", srv.Name)
		output.PrintData(json.RawMessage(resp.Data), resolveFormat())
		fetchAndCacheOnLogin()
		ref, _ := cmd.Flags().GetString("ref")
		reportInstallRef(ref, data.AgentID, email)
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
			Email       string `json:"email"`
			ExpiresAt   int64  `json:"expires_at"`
		}
		if err := json.Unmarshal(resp.Data, &data); err != nil {
			return fmt.Errorf("parse verify response: %w", err)
		}
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		srv, err := cfg.GetActive(serverFlag)
		if err != nil {
			return fmt.Errorf("%w", err)
		}
		err = auth.SaveCredentials(srv.Name, &auth.Credentials{
			AccessToken: data.AccessToken,
			Email:       data.Email,
			AgentID:     data.AgentID,
			ExpiresAt:   data.ExpiresAt,
		})
		if err != nil {
			return fmt.Errorf("save credentials: %w", err)
		}
		output.PrintMessage("Logged in successfully to server %q", srv.Name)
		output.PrintData(json.RawMessage(resp.Data), resolveFormat())
		fetchAndCacheOnLogin()
		ref, _ := cmd.Flags().GetString("ref")
		reportInstallRef(ref, data.AgentID, data.Email)
		return nil
	},
}

var authLogoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Log out from current server",
	Long: `Log out by revoking the access token on the server and removing local credentials.

Examples:
  eigenflux auth logout
  eigenflux auth logout --server staging`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		srv, err := cfg.GetActive(serverFlag)
		if err != nil {
			return fmt.Errorf("%w", err)
		}

		// Best-effort server-side logout.
		creds, _ := auth.LoadCredentials(srv.Name)
		if creds != nil && creds.AccessToken != "" {
			c := newClient()
			c.Post("/auth/logout", nil)
		}

		// Remove local credentials.
		auth.DeleteCredentials(srv.Name)

		// Remove cached profile and contacts.
		cache.DeleteProfileAndContacts(srv.Name)

		output.PrintMessage("Logged out from server %q", srv.Name)
		return nil
	},
}

// fetchAndCacheOnLogin fetches profile and contacts, caching both locally (best-effort).
func fetchAndCacheOnLogin() {
	c := newClient()
	if resp, err := c.Get("/agents/me", nil); err == nil && resp.Code == 0 {
		cacheProfile(resp.Data)
	}
	if resp, err := c.Get("/relations/friends", nil); err == nil && resp.Code == 0 {
		cacheContacts(resp.Data)
	}
}

func init() {
	authLoginCmd.Flags().String("email", "", "email address to log in with (required)")
	authLoginCmd.Flags().String("ref", "", "referral code (EF-xxxxxxxx) from the /install page, for attribution (optional)")
	authLoginCmd.Flags().Bool("force", false, "replace credentials even if this home already holds a different identity")
	authVerifyCmd.Flags().String("challenge-id", "", "challenge ID from login response (required)")
	authVerifyCmd.Flags().String("code", "", "OTP code from email (required)")
	authVerifyCmd.Flags().String("ref", "", "referral code (EF-xxxxxxxx) from the /install page, for attribution (optional)")
	authCmd.AddCommand(authLoginCmd, authVerifyCmd, authLogoutCmd)
	rootCmd.AddCommand(authCmd)
}
