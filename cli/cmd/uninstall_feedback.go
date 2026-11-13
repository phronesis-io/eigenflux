package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/maintenance"
	"github.com/spf13/cobra"
)

const uninstallReasonLimit = 1000

func readUninstallReason(cmd *cobra.Command, submitting bool) (string, error) {
	reason, _ := cmd.Flags().GetString("reason")
	fromStdin, _ := cmd.Flags().GetBool("reason-stdin")
	if !submitting {
		return "", nil
	}
	if !fromStdin {
		return reason, nil
	}
	if cmd.Flags().Changed("reason") {
		return "", errors.New("use only one of --reason-stdin and --reason")
	}
	data, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), uninstallReasonLimit+1))
	if err != nil {
		return "", fmt.Errorf("read uninstall reason: %w", err)
	}
	return string(data), nil
}

func validateUninstallReason(reason, category string) error {
	if category == "declined" {
		if reason != "" {
			return errors.New("declined uninstall feedback cannot include reason text")
		}
		return nil
	}
	if category == "not_provided" || strings.TrimSpace(reason) == "" {
		return errors.New("provide an uninstall reason, or use --reason-category declined if the owner explicitly declined")
	}
	if len(reason) > uninstallReasonLimit || !utf8.ValidString(reason) || strings.ContainsRune(reason, 0) {
		return errors.New("uninstall reason must be valid UTF-8, at most 1000 bytes, and contain no NUL characters")
	}
	return nil
}

func (o *uninstallObservation) submitFeedback(reason string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	server, err := cfg.GetActive(serverFlag)
	if err != nil {
		return err
	}
	homePath, err := filepath.EvalSymlinks(config.HomeDir())
	if err != nil {
		return err
	}
	var scope maintenance.Scope
	for _, candidate := range o.Scopes {
		if candidate.Home == homePath && candidate.Server == server.Name {
			scope = candidate
			break
		}
	}
	if scope.AgentID == "" {
		return errors.New("uninstall feedback requires a signed-in Agent in the selected installation and server")
	}
	regular, err := auth.HasV2Credentials(server.Name)
	if err != nil || !regular {
		return errors.New("uninstall feedback requires regular Agent credentials")
	}
	credentials, err := auth.LoadV2Credentials(server.Name)
	if err != nil || credentials.AgentID != scope.AgentID {
		return errors.New("uninstall feedback requires a signed-in Agent in the selected installation and server")
	}
	if o.FeedbackServer != "" && (o.FeedbackServer != server.Name || o.FeedbackAgentID != credentials.AgentID || o.FeedbackEndpoint != server.Endpoint) {
		return errors.New("uninstall feedback must be retried with the original Agent and server")
	}

	path, err := uninstallReceiptPath(o.AttemptID)
	if err != nil {
		return err
	}
	o.FeedbackServer, o.FeedbackAgentID, o.FeedbackEndpoint = server.Name, credentials.AgentID, server.Endpoint
	o.Pending = true
	// Persist the retry identity before the request. Feedback text stays in memory.
	if err := saveMaintenance(path, o); err != nil {
		return err
	}

	event := maintenance.NewEvent(o.AttemptID, "cli", "manual", "uninstall", "started")
	event.EventAt = o.Started.UnixMilli()
	event.Source, event.Platform, event.ReasonCategory = o.Source, runtime.GOOS, o.ReasonCategory
	event.FromVersion = o.Version
	for _, home := range o.Homes {
		if home.Home == scope.Home {
			event.Host = runtimeProduct(home.Host)
			break
		}
	}
	c := client.New(strings.TrimRight(server.Endpoint, "/")+"/api/v2", credentials.AccessToken, o.Version, clientMetaForServer(server))
	c.HTTPClient.Timeout = 5 * time.Second
	c.HTTPClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := c.Post("/maintenance/events:batch", map[string]interface{}{
		"events":             []maintenance.Event{event},
		"uninstall_feedback": map[string]string{"reason": reason},
	})
	if err != nil {
		var apiErr *client.APIError
		if errors.As(err, &apiErr) {
			if apiErr.StatusCode == http.StatusUnauthorized {
				return fmt.Errorf("uninstall feedback requires valid authentication; refresh the Agent session and retry --begin --attempt %s", o.AttemptID)
			}
			return fmt.Errorf("uninstall feedback upload failed (HTTP %d); retry --begin --attempt %s with the same feedback", apiErr.StatusCode, o.AttemptID)
		}
		return fmt.Errorf("uninstall feedback upload failed; check connectivity and retry --begin --attempt %s with the same feedback", o.AttemptID)
	}
	var receipt struct {
		AttemptID string `json:"uninstall_feedback_attempt_id"`
	}
	if response.HTTPStatus != http.StatusAccepted || response.Code != 0 ||
		json.Unmarshal(response.Data, &receipt) != nil || receipt.AttemptID != o.AttemptID {
		return fmt.Errorf("server did not confirm uninstall feedback; retry --begin --attempt %s after the server supports feedback", o.AttemptID)
	}
	o.FeedbackUploaded = true
	if err := saveMaintenance(path, o); err != nil {
		o.FeedbackUploaded = false
		return fmt.Errorf("save uninstall feedback confirmation; retry --begin --attempt %s: %w", o.AttemptID, err)
	}
	return nil
}
