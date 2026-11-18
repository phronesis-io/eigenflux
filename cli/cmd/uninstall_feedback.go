package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
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

// feedbackTarget resolves the invoking registered Home and pins the selected
// identity. An absent V2 credential file means an unregistered installation.
// A present but expired or damaged session never falls back to anonymous.
func (o *uninstallObservation) feedbackTarget() (*config.Server, string, string, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, "", "", err
	}
	selected := serverFlag
	if selected == "" {
		selected = o.FeedbackServer
	}
	server, err := cfg.GetActive(selected)
	if err != nil {
		return nil, "", "", err
	}
	if server.Name == "." || server.Name == ".." || strings.ContainsAny(server.Name, "\\/") {
		return nil, "", "", errors.New("invalid uninstall feedback server name")
	}
	endpoint, err := url.Parse(server.Endpoint)
	if err != nil || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" ||
		(endpoint.Scheme != "https" && !(endpoint.Scheme == "http" &&
			(endpoint.Hostname() == "localhost" || net.ParseIP(endpoint.Hostname()).IsLoopback()))) {
		return nil, "", "", errors.New("uninstall feedback requires an HTTPS server or an HTTP loopback endpoint")
	}
	homePath, err := filepath.EvalSymlinks(config.HomeDir())
	if err != nil {
		return nil, "", "", err
	}
	registered := false
	for _, home := range o.Homes {
		if home.Home == homePath {
			registered = true
			break
		}
	}
	if !registered {
		return nil, "", "", errors.New("uninstall feedback requires the invoking Home to be registered")
	}
	var id, token string
	present, err := auth.HasV2Credentials(server.Name)
	if err != nil {
		return nil, "", "", err
	}
	if present {
		credentials, err := auth.LoadV2Credentials(server.Name)
		if err != nil {
			return nil, "", "", errors.New("repair the selected Agent credentials before uninstalling")
		}
		id, token = credentials.AgentID, credentials.AccessToken
	}
	if o.FeedbackServer != "" && (o.FeedbackServer != server.Name || o.FeedbackAgentID != id || o.FeedbackEndpoint != server.Endpoint) {
		return nil, "", "", errors.New("uninstall feedback is bound to the original Agent and server; start a new attempt")
	}
	return server, id, token, nil
}

func (o *uninstallObservation) feedbackClient(token string, meta client.Meta) *client.Client {
	c := client.New(strings.TrimRight(o.FeedbackEndpoint, "/")+"/api/v2", token, o.Version, meta)
	c.HTTPClient.Timeout = 5 * time.Second
	c.HTTPClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return c
}

func (o *uninstallObservation) submitFeedback(reason string) error {
	server, id, token, err := o.feedbackTarget()
	if err != nil {
		return err
	}

	path, err := uninstallReceiptPath(o.AttemptID)
	if err != nil {
		return err
	}
	o.FeedbackServer, o.FeedbackAgentID, o.FeedbackEndpoint = server.Name, id, server.Endpoint
	if id == "" && o.FeedbackProof == "" {
		o.FeedbackProof = maintenance.NewID()
	}
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
		if home.Home == config.HomeDir() {
			event.Host = runtimeProduct(home.Host)
			break
		}
	}
	c := o.feedbackClient(token, clientMetaForServer(server))
	var response *client.APIResponse
	if id == "" {
		response, err = c.Post("/maintenance/unregistered-uninstall", map[string]interface{}{
			"events":             []maintenance.Event{event},
			"uninstall_feedback": map[string]string{"reason": reason, "proof": o.FeedbackProof},
		})
	} else {
		response, err = postRegisteredUninstall(c, event, reason)
	}
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

// Unregistered installations have no Agent token. Their attempt proof is
// narrowly scoped to reporting outcomes for the previously accepted feedback.
func (o *uninstallObservation) reportUnregisteredOutcome(result, code string) {
	if !o.FeedbackUploaded || o.FeedbackProof == "" {
		return
	}
	event := maintenance.NewEvent(o.AttemptID, "cli", "manual", "uninstall", result)
	event.Source, event.Platform, event.ReasonCategory = o.Source, runtime.GOOS, o.ReasonCategory
	event.FromVersion, event.ErrorCode = o.Version, code
	event.DurationMS = time.Since(o.Started).Milliseconds()
	c := o.feedbackClient("", client.Meta{})
	response, err := c.Post("/maintenance/unregistered-uninstall", map[string]interface{}{
		"events":             []maintenance.Event{event},
		"uninstall_feedback": map[string]string{"proof": o.FeedbackProof},
	})
	if err != nil || response.HTTPStatus != http.StatusAccepted {
		fmt.Fprintln(os.Stderr, "Uninstall outcome upload failed.")
	}
}

func postRegisteredUninstall(c *client.Client, event maintenance.Event, reason string) (*client.APIResponse, error) {
	body := map[string]interface{}{
		"events":             []maintenance.Event{event},
		"uninstall_feedback": map[string]string{"reason": reason},
	}
	response, err := c.Post("/maintenance/registered-uninstall", body)
	var apiErr *client.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
		if event.Result == "started" {
			return c.Post("/maintenance/events:batch", body)
		}
		return postMaintenanceEvents(c, []maintenance.Event{event})
	}
	return response, err
}

// The dedicated endpoint validates outcomes against the original feedback.
func (o *uninstallObservation) postUninstallOutcomes(c *client.Client, scope maintenance.Scope, endpoint string, events []maintenance.Event) error {
	if !o.FeedbackUploaded || o.FeedbackAgentID == "" || scope.AgentID != o.FeedbackAgentID ||
		scope.Server != o.FeedbackServer || endpoint != o.FeedbackEndpoint {
		_, err := postMaintenanceEvents(c, events)
		return err
	}
	for _, event := range events {
		response, err := postRegisteredUninstall(c, event, "")
		if err != nil {
			return err
		}
		if response.HTTPStatus != http.StatusAccepted || response.Code != 0 {
			return errors.New("uninstall outcome was not accepted")
		}
	}
	return nil
}
