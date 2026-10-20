package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

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
}

func reportCommandFailure(w io.Writer, err error) int {
	fmt.Fprintln(w, err)
	var apiErr *client.APIError
	if !v2CredentialNeedsLogin(err) {
		return output.ExitUsageError
	}
	_ = errors.As(err, &apiErr)
	code := "IDENTITY_UNAVAILABLE"
	if apiErr != nil {
		code = apiErr.ErrorCode
		if code == "" {
			code = "AUTH_REQUIRED"
		}
	}
	notice := authFailureNotice{
		SchemaVersion: "eigenflux_auth_required.v1", Status: "auth_required", Code: code,
		Home: config.HomeDir(), Server: activeServerName(), NotificationRequired: true, ChecksBlocked: true,
		Instruction: "Notify the owner that EigenFlux checks have stopped. Use the host failure protocol (NOTIFY in Codex), even when this failure was previously reported. Follow ef-profile in this exact Home and server. Restore the original identity when unavailable; require owner review for revoked or invalid credentials. Do not repeat automatic recovery or create or switch an account.",
	}
	_ = json.NewEncoder(w).Encode(notice)
	return output.ExitAuthRequired
}

func exitCommandFailure(err error) {
	os.Exit(reportCommandFailure(os.Stderr, err))
}
