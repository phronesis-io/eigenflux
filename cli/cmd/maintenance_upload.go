package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/maintenance"
)

const (
	watchFailuresHeader   = "X-EigenFlux-Watch-Failures"
	runtimeFailuresHeader = "X-EigenFlux-Runtime-Failures"
)

// flushBackgroundMaintenance uses the same permission gate for scheduled and
// foreground-triggered background uploads.
func flushBackgroundMaintenance(scope maintenance.Scope, endpoint string, cached *auth.V2Credentials, openClient func() (*client.Client, error), sent func([]maintenance.Event)) error {
	return flushMaintenanceRequest(scope, endpoint, cached, func(events []maintenance.Event) error {
		c, err := openClient()
		if err != nil {
			return err
		}
		c.HTTPClient.Timeout = 5 * time.Second
		_, err = postMaintenanceEvents(c, events)
		if err == nil && sent != nil {
			sent(events)
		}
		return err
	})
}

func flushMaintenanceRequest(scope maintenance.Scope, endpoint string, cached *auth.V2Credentials, send func([]maintenance.Event) error) error {
	var credential string
	var restricted bool
	if cached != nil {
		restricted = cached.Scopes != nil && !slices.Contains(cached.Scopes, "settings:write")
		sum := sha256.Sum256([]byte(strings.TrimRight(endpoint, "/") + "/api/v2\x00" + cached.AccessToken + "\x00" + strconv.FormatBool(restricted)))
		credential = hex.EncodeToString(sum[:])
	}
	return maintenance.FlushForCredential(scope, time.Now(), credential, restricted, func(events []maintenance.Event) error {
		err := send(events)
		var apiErr *client.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusConflict && apiErr.ErrorCode == "ONBOARDING_REQUIRED" {
			return maintenance.ErrUploadOnboarding
		}
		return err
	})
}

// Listing observations have their own journal, request and backoff. Use the
// current cached token without business callbacks or credential refresh.
func flushListingBroadcastMaintenance(scope maintenance.Scope, principal, endpoint string, cached *auth.V2Credentials) {
	if cached == nil || cached.AgentID != scope.AgentID || cached.ExpiresAt <= time.Now().UnixMilli() ||
		!maintenance.HasListingBroadcastJournal(scope) {
		return
	}
	_ = flushMaintenanceRequest(scope.ForListingBroadcast(), endpoint, cached, func(events []maintenance.Event) error {
		current, err := maintenanceIdentity(scope, principal, endpoint)
		if err != nil {
			return err
		}
		if current.ExpiresAt <= time.Now().UnixMilli() {
			return errors.New("listing broadcast upload awaits valid credentials")
		}
		c := client.New(strings.TrimRight(endpoint, "/")+"/api/v2", current.AccessToken, version, clientMetaForServerName(scope.Server))
		c.HTTPClient.Timeout = 5 * time.Second
		response, err := c.Post("/maintenance/listing-broadcasts:batch", map[string][]maintenance.Event{"events": events})
		if err != nil {
			return err
		}
		var receipt struct {
			AcceptedEvents *int `json:"accepted_events"`
		}
		if response.HTTPStatus != http.StatusAccepted || response.Code != 0 ||
			json.Unmarshal(response.Data, &receipt) != nil || receipt.AcceptedEvents == nil ||
			*receipt.AcceptedEvents < 0 || *receipt.AcceptedEvents > len(events) {
			return errors.New("listing broadcast upload unconfirmed")
		}
		return nil
	})
}

// Keep the original wire schema so an older gateway can accept the full batch.
// Detail stays in the local journal and travels as optional request metadata.
func postMaintenanceEvents(c *client.Client, events []maintenance.Event) (*client.APIResponse, error) {
	wire := append([]maintenance.Event(nil), events...)
	watchFailures := make(map[string]string)
	runtimeFailures := make(map[string]string)
	for i := range wire {
		event := &wire[i]
		if event.Runtime != nil && event.ErrorCode != "" {
			if maintenance.RuntimeFailureCode(event.ErrorCode) {
				runtimeFailures[event.EventID] = event.ErrorCode
			}
			event.ErrorCode = ""
		}
		if event.OuterLoop != nil && event.OuterLoop.FailureReason != "" {
			watchFailures[event.EventID] = event.OuterLoop.FailureReason
			outer := *event.OuterLoop
			outer.FailureReason = ""
			event.OuterLoop = &outer
		}
	}
	headers := make(map[string]string)
	setMaintenanceFailureHeader(headers, watchFailuresHeader, watchFailures)
	setMaintenanceFailureHeader(headers, runtimeFailuresHeader, runtimeFailures)
	post := func(batch []maintenance.Event) (*client.APIResponse, error) {
		return c.PostWithHeaders("/maintenance/events:batch", map[string]interface{}{"events": batch}, headers)
	}
	response, err := post(wire)
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.ErrorCode != "INVALID_MAINTENANCE_EVENT" ||
		apiErr.Msg != "uninstall metadata requires uninstall phase" {
		return response, err
	}
	// Older gateways reject the new phase before writing any events. Keep
	// existing maintenance uploads working if the gateway rolls back.
	legacy := wire[:0]
	for _, event := range wire {
		if event.Phase != "pm_owner_notice" {
			legacy = append(legacy, event)
		}
	}
	if len(legacy) == len(events) {
		return response, err
	}
	if len(legacy) == 0 {
		return nil, nil
	}
	return post(legacy)
}

func setMaintenanceFailureHeader(headers map[string]string, name string, failures map[string]string) {
	if len(failures) == 0 || len(failures) > 50 {
		return
	}
	raw, err := json.Marshal(failures)
	if err == nil && len(raw) <= 8192 {
		headers[name] = string(raw)
	}
}
