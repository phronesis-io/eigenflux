package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/installflow"
	"cli.eigenflux.ai/internal/output"
	"github.com/spf13/cobra"
)

func installScope(server config.Server) installflow.Scope {
	return installflow.Scope{Home: config.HomeDir(), Endpoint: server.Endpoint}
}

func installObservationHeaders(server config.Server) map[string]string {
	if os.Getenv("EIGENFLUX_INSTALL_OBSERVATIONS") == "0" {
		return nil
	}
	s, err := installflow.Current(installScope(server))
	if err != nil {
		return nil
	}
	return map[string]string{"X-Install-Session-ID": s.ID, "X-Install-Session-Proof": s.Proof}
}

type installationPoster struct {
	client *client.Client
	server config.Server
}

func (p installationPoster) Post(path string, body interface{}) (*client.APIResponse, error) {
	copy := *p.client
	httpCopy := *p.client.HTTPClient
	httpCopy.CheckRedirect = rejectInstallRedirect
	copy.HTTPClient = &httpCopy
	return copy.PostWithHeaders(path, body, installObservationHeaders(p.server))
}

func rejectInstallRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

func getInstallContext(c *client.Client, server config.Server, params map[string]string) (*client.APIResponse, error) {
	headers := installObservationHeaders(server)
	if len(headers) == 0 {
		return c.Get("/agent-context", params)
	}
	session, err := installflow.Current(installScope(server))
	agentID, _ := cachedInstallCredentials(server)
	if err != nil || agentID == "" || (session.AgentID != "" && session.AgentID != agentID) {
		return c.Get("/agent-context", params)
	}
	copy := *c
	httpCopy := *c.HTTPClient
	httpCopy.CheckRedirect = rejectInstallRedirect
	copy.HTTPClient = &httpCopy
	return copy.GetWithHeaders("/agent-context", params, headers)
}

func flushInstallObservations(server config.Server, agentID, token string, targetEvent ...string) error {
	if os.Getenv("EIGENFLUX_INSTALL_OBSERVATIONS") == "0" {
		return fmt.Errorf("installation observations disabled")
	}
	confirmed := len(targetEvent) == 0
	err := installflow.Flush(installScope(server), agentID, func(batch installflow.Batch, headers map[string]string, authenticated bool) error {
		path := "/install-telemetry/events:batch"
		if authenticated {
			path = "/agents/me/install-telemetry/events:batch"
		}
		c := client.New(strings.TrimRight(server.Endpoint, "/")+"/api/v2", token, version, client.Meta{})
		c.HTTPClient.Timeout = 3 * time.Second
		c.HTTPClient.CheckRedirect = rejectInstallRedirect
		response, err := c.PostWithHeaders(path, batch, headers)
		if err != nil {
			return err
		}
		var receipt struct {
			Accepted  *int   `json:"accepted_events"`
			SessionID string `json:"install_session_id"`
			Bound     *bool  `json:"bound"`
		}
		if response.HTTPStatus != 202 || response.Code != 0 || json.Unmarshal(response.Data, &receipt) != nil || receipt.Accepted == nil || *receipt.Accepted < 0 || *receipt.Accepted > len(batch.Events) || receipt.SessionID != batch.SessionID || receipt.Bound == nil || (authenticated && !*receipt.Bound) {
			return fmt.Errorf("invalid installation observation acknowledgement")
		}
		for _, event := range batch.Events {
			if len(targetEvent) > 0 && event.ID == targetEvent[0] {
				confirmed = true
			}
		}
		return nil
	})
	if err == nil && !confirmed {
		return fmt.Errorf("observation remains queued for a later batch")
	}
	return err
}

func cachedInstallCredentials(server config.Server) (string, string) {
	c, err := auth.LoadV2Credentials(server.Name)
	if err != nil || c.ExpiresAt <= time.Now().UnixMilli() {
		return "", ""
	}
	return c.AgentID, c.AccessToken
}

func observeInstall(server config.Server, event installflow.Event) {
	agentID, token := cachedInstallCredentials(server)
	observeInstallAuthenticated(server, agentID, token, event)
}

func observeInstallAuthenticated(server config.Server, agentID, token string, event installflow.Event) {
	if os.Getenv("EIGENFLUX_INSTALL_OBSERVATIONS") == "0" {
		return
	}
	if _, err := installflow.Current(installScope(server)); err != nil {
		return
	}
	event = installRuntimeMetadata(server, event)
	if _, err := installflow.RecordForAgent(installScope(server), agentID, event); err != nil {
		return
	}
	// Measurements never change whether an installation or business action succeeds.
	_ = flushInstallObservations(server, agentID, token)
}

func installRuntimeMetadata(server config.Server, event installflow.Event) installflow.Event {
	meta := clientMetaForServer(&server)
	event.RuntimeName = installflow.RuntimeName(meta.Host)
	switch meta.Mode {
	case "skill", "plugin", "cli":
		event.RuntimeMode = meta.Mode
	default:
		event.RuntimeMode = "unknown"
	}
	return event
}

func installEvent(eventType, result string) installflow.Event {
	return installflow.Event{Type: eventType, Result: result}
}

// Called only after a fresh network response is validated and rendered. Cache
// reuse and completed accounts receiving baseline fallback are not onboarding.
func observeBaselineFeed(server config.Server, payload json.RawMessage) {
	var feed struct {
		Schema          string `json:"schema_version"`
		Personalization struct {
			Mode  string `json:"mode"`
			State string `json:"onboarding_state"`
		} `json:"personalization"`
		Items []json.RawMessage `json:"items"`
	}
	if json.Unmarshal(payload, &feed) != nil || feed.Schema != "feed.v2" || feed.Personalization.Mode != "baseline" || feed.Personalization.State != "in_progress" || feed.Items == nil {
		return
	}
	count := len(feed.Items)
	event := installEvent("baseline_feed", "succeeded")
	event.Count = &count
	observeInstall(server, event)
}

func newOnboardingStartCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "onboarding-start", Short: "Start or resume an installation observation session in the current Home", Args: cobra.NoArgs}
	cmd.Flags().Bool("new-attempt", false, "Begin a distinct initial-install attempt instead of resuming the current one")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		if observationsDisabled(cmd) {
			return nil
		}
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		server, err := cfg.GetActive(serverFlag)
		if err != nil {
			return err
		}
		fresh, _ := cmd.Flags().GetBool("new-attempt")
		if _, stateErr := installflow.Current(installScope(*server)); os.IsNotExist(stateErr) || fresh {
			if exists, credErr := auth.HasV2Credentials(server.Name); credErr != nil || exists {
				return fmt.Errorf("do not start a new installation attempt for an existing Agent")
			}
			if _, legacyErr := auth.LoadCredentials(server.Name); !errors.Is(legacyErr, os.ErrNotExist) {
				return fmt.Errorf("do not start a new installation attempt for an existing Agent")
			}
		}
		session, err := installflow.Start(installScope(*server), fresh)
		if err != nil {
			return err
		}
		observeInstall(*server, installEvent("install_started", "started"))
		host := installRuntimeMetadata(*server, installEvent("host_detected", "succeeded"))
		if host.RuntimeName == "unknown" {
			host.Result = "pending"
		}
		observeInstall(*server, host)
		output.PrintDataTo(cmd.OutOrStdout(), map[string]any{"install_session_id": session.ID, "observation": "client_reported", "business_permissions_changed": false}, resolveFormat())
		return nil
	}
	return cmd
}

func newOnboardingObserveCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "onboarding-observe", Short: "Record an observed onboarding outcome without repeating business work", Args: cobra.NoArgs}
	cmd.Flags().String("event", "", "Installation lifecycle event type")
	cmd.Flags().String("result", "", "started, succeeded, failed, declined, skipped, or pending")
	cmd.Flags().String("attempt", "", "Existing correlation ID; omitted first-check requests allocate one")
	cmd.Flags().String("component", "", "cli, skills, plugin, scheduler, identity, or console")
	cmd.Flags().String("choice", "", "prefill or manual, only for profile_mode")
	cmd.Flags().String("reason", "", "Fixed observation reason; never pass error messages, paths or content")
	cmd.Flags().Int("count", 0, "Non-sensitive item count, when actually observed")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		if observationsDisabled(cmd) {
			return nil
		}
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		server, err := cfg.GetActive(serverFlag)
		if err != nil {
			return err
		}
		eventType, _ := cmd.Flags().GetString("event")
		result, _ := cmd.Flags().GetString("result")
		event := installEvent(eventType, result)
		event.AttemptID, _ = cmd.Flags().GetString("attempt")
		event.Component, _ = cmd.Flags().GetString("component")
		event.Choice, _ = cmd.Flags().GetString("choice")
		event.Reason, _ = cmd.Flags().GetString("reason")
		if cmd.Flags().Changed("count") {
			count, _ := cmd.Flags().GetInt("count")
			event.Count = &count
		}
		if eventType == "first_check_requested" && event.AttemptID == "" {
			event.AttemptID, err = installflow.NewID()
			if err != nil {
				return err
			}
		}
		if (eventType == "first_check" || eventType == "first_check_result") && event.AttemptID == "" {
			return fmt.Errorf("--attempt from first_check_requested is required")
		}
		event = installRuntimeMetadata(*server, event)
		agentID, token := cachedInstallCredentials(*server)
		event, err = installflow.RecordForAgent(installScope(*server), agentID, event)
		if err != nil {
			return err
		}
		status := "confirmed"
		if flushInstallObservations(*server, agentID, token, event.ID) != nil {
			status = "pending"
		}
		output.PrintDataTo(cmd.OutOrStdout(), map[string]any{"recorded": true, "event_id": event.ID, "attempt_id": event.AttemptID, "upload": status, "evidence": "client_reported"}, resolveFormat())
		return nil
	}
	return cmd
}

func init() {
	agentV2Cmd.AddCommand(newOnboardingStartCommand(), newOnboardingObserveCommand())
}

func observationsDisabled(cmd *cobra.Command) bool {
	if os.Getenv("EIGENFLUX_INSTALL_OBSERVATIONS") != "0" {
		return false
	}
	output.PrintDataTo(cmd.OutOrStdout(), map[string]any{"recorded": false, "upload": "disabled", "business_permissions_changed": false}, resolveFormat())
	return true
}
