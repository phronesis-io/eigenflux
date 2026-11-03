package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/output"
)

type authFailureNotice struct {
	SchemaVersion        string `json:"schema_version"`
	Status               string `json:"status"`
	Code                 string `json:"code"`
	Home                 string `json:"home"`
	Server               string `json:"server"`
	NotificationRequired bool   `json:"notification_required"`
	ChecksBlocked        bool   `json:"checks_blocked"`
	Instruction          string `json:"instruction"`
	OwnerAction          string `json:"owner_action,omitempty"`
}

type authUnavailableNotice struct {
	SchemaVersion string `json:"schema_version"`
	Status        string `json:"status"`
	Code          string `json:"code"`
	Home          string `json:"home"`
	Server        string `json:"server"`
	ChecksBlocked bool   `json:"checks_blocked"`
	LoginRequired bool   `json:"login_required"`
	Retryable     bool   `json:"retryable"`
}

func isAgentAuthUnavailable(err error) bool {
	var apiErr *client.APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusServiceUnavailable && apiErr.ErrorCode == "AGENT_AUTH_UNAVAILABLE"
}

func reportAuthUnavailable(w io.Writer, err error, apiErr *client.APIError) {
	// Preserve the distinction between credential acquisition failures and an
	// unrelated business endpoint failing after authentication succeeded.
	if !isV2CredentialError(err) && !isAgentAuthUnavailable(err) {
		return
	}
	if apiErr != nil && apiErr.StatusCode < http.StatusInternalServerError && apiErr.StatusCode != http.StatusTooManyRequests {
		return
	}
	code := "AUTH_CREDENTIALS_UNAVAILABLE"
	if apiErr != nil {
		code = apiErr.ErrorCode
		if code == "" {
			code = "AUTH_SERVICE_UNAVAILABLE"
		}
	} else {
		var networkErr net.Error
		if errors.As(err, &networkErr) {
			code = "AUTH_NETWORK_UNAVAILABLE"
		}
	}
	_ = json.NewEncoder(w).Encode(authUnavailableNotice{
		SchemaVersion: "eigenflux_auth_unavailable.v1", Status: "auth_unavailable", Code: code,
		Home: config.HomeDir(), Server: activeServerName(), ChecksBlocked: true,
		LoginRequired: false, Retryable: true,
	})
}

func reportCommandFailure(w io.Writer, err error) int {
	fmt.Fprintln(w, err)
	var apiErr *client.APIError
	_ = errors.As(err, &apiErr)
	if !v2CredentialNeedsLogin(err) {
		reportAuthUnavailable(w, err, apiErr)
		return output.ExitUsageError
	}
	code := "IDENTITY_UNAVAILABLE"
	if apiErr != nil {
		code = apiErr.ErrorCode
		if code == "" {
			code = "AUTH_REQUIRED"
		}
	}
	serverName := activeServerName()
	notice := authFailureNotice{
		SchemaVersion: "eigenflux_auth_required.v1", Status: "auth_required", Code: code,
		Home: config.HomeDir(), Server: serverName, NotificationRequired: true, ChecksBlocked: true,
		Instruction: "Notify the owner that EigenFlux checks have stopped. Use the host failure protocol (NOTIFY in Codex), even when this failure was previously reported. Follow ef-profile in this exact Home and server. Restore the original identity when unavailable; require owner review for revoked or invalid credentials. Do not repeat automatic recovery or create or switch an account.",
		OwnerAction: authFailureOwnerAction(err, serverName),
	}
	_ = json.NewEncoder(w).Encode(notice)
	return output.ExitAuthRequired
}

func authFailureOwnerAction(err error, serverName string) string {
	if isV2IdentityUnavailable(err) {
		return `Reply in this conversation with "Inspect and restore my original EigenFlux identity". Your Agent will check file access permissions in the Home and server shown above. If the files are missing, restore the original identity.json and agent-v2-credentials.json from the existing installation or a backup. Do not initialize a replacement identity.`
	}
	hasV2, inspectErr := auth.HasV2Credentials(serverName)
	if inspectErr != nil || !hasV2 {
		return ""
	}
	return `Reply in this conversation with "Recover my current EigenFlux account" to authorize foreground recovery of the same account in the Home and server shown above. If you intentionally revoked access, first confirm that you want to authorize it again. Do not repeat login automatically or create or switch accounts.`
}

func exitCommandFailure(err error) {
	os.Exit(reportCommandFailure(os.Stderr, err))
}
