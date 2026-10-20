package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/output"
)

func TestCommandFailureRequiresOwnerNotificationWhileAuthenticationIsBlocked(t *testing.T) {
	tempHome(t)
	oldServer := serverFlag
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	serverFlag = cfg.DefaultServer
	t.Cleanup(func() { serverFlag = oldServer })
	for _, failure := range []error{
		fmt.Errorf("refresh: %w", &client.APIError{StatusCode: http.StatusUnauthorized, ErrorCode: "REFRESH_INVALID"}),
		unavailableV2Identity(errors.New("original key missing")),
	} {
		for cycle := 0; cycle < 2; cycle++ {
			var out bytes.Buffer
			if code := reportCommandFailure(&out, failure); code != output.ExitAuthRequired {
				t.Fatalf("exit=%d output=%s", code, &out)
			}
			lines := strings.Split(strings.TrimSpace(out.String()), "\n")
			var notice authFailureNotice
			if err := json.Unmarshal([]byte(lines[len(lines)-1]), &notice); err != nil {
				t.Fatal(err)
			}
			if notice.SchemaVersion != "eigenflux_auth_required.v1" || notice.Status != "auth_required" ||
				!notice.NotificationRequired || !notice.ChecksBlocked || notice.Home != config.HomeDir() || notice.Server != serverFlag {
				t.Fatalf("failure instruction lost identity or required notification: %+v", notice)
			}
			if !strings.Contains(notice.Instruction, "NOTIFY") || !strings.Contains(notice.Instruction, "Do not repeat automatic recovery") {
				t.Fatalf("missing host failure/recovery guidance: %+v", notice)
			}
		}
	}
}

func TestCommandFailureDoesNotClassifyServiceAndPermissionErrorsAsLoginRequired(t *testing.T) {
	for _, err := range []error{
		wrapV2CredentialError(&client.APIError{StatusCode: 503, ErrorCode: "AGENT_AUTH_UNAVAILABLE"}),
		wrapV2CredentialError(errors.New("connection refused")),
		&client.APIError{StatusCode: 403, ErrorCode: "AGENT_SCOPE_REQUIRED"},
		&client.APIError{StatusCode: 409, ErrorCode: "ONBOARDING_REQUIRED"},
	} {
		var out bytes.Buffer
		if code := reportCommandFailure(&out, err); code != output.ExitUsageError || strings.Contains(out.String(), "auth_required") {
			t.Fatalf("service/permission error became expired login: exit=%d output=%s", code, &out)
		}
	}
}

func TestCommandFailureReportsCredentialServiceUnavailableWithoutLogin(t *testing.T) {
	tempHome(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	oldServer := serverFlag
	serverFlag = cfg.DefaultServer
	t.Cleanup(func() { serverFlag = oldServer })
	for _, tc := range []struct {
		name, code string
		err        error
	}{
		{"refresh service", "AGENT_AUTH_UNAVAILABLE", wrapV2CredentialError(&client.APIError{StatusCode: 503, ErrorCode: "AGENT_AUTH_UNAVAILABLE"})},
		{"access validator", "AGENT_AUTH_UNAVAILABLE", &client.APIError{StatusCode: 503, ErrorCode: "AGENT_AUTH_UNAVAILABLE"}},
		{"registration service", "REGISTRATION_UNAVAILABLE", wrapV2CredentialError(&client.APIError{StatusCode: 503, ErrorCode: "REGISTRATION_UNAVAILABLE"})},
		{"credential network", "AUTH_NETWORK_UNAVAILABLE", wrapV2CredentialError(&url.Error{Op: "Post", URL: "http://127.0.0.1:1/refresh", Err: errors.New("connection refused")})},
		{"credential protocol", "AUTH_CREDENTIALS_UNAVAILABLE", wrapV2CredentialError(errors.New("invalid refresh response"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			if exit := reportCommandFailure(&out, tc.err); exit != output.ExitUsageError {
				t.Fatalf("temporary auth failure exit=%d, want non-login exit", exit)
			}
			lines := strings.Split(strings.TrimSpace(out.String()), "\n")
			var notice authUnavailableNotice
			if err := json.Unmarshal([]byte(lines[len(lines)-1]), &notice); err != nil {
				t.Fatalf("missing auth service failure contract: %v output=%s", err, &out)
			}
			if notice.SchemaVersion != "eigenflux_auth_unavailable.v1" || notice.Status != "auth_unavailable" ||
				notice.Code != tc.code || !notice.ChecksBlocked || notice.LoginRequired || !notice.Retryable ||
				notice.Home != config.HomeDir() || notice.Server != serverFlag {
				t.Fatalf("incorrect temporary failure contract: %+v", notice)
			}
		})
	}
}

func TestCommandFailureDoesNotMarkUnrelatedEndpointFailuresAsCredentialUnavailable(t *testing.T) {
	for _, failure := range []error{
		&client.APIError{StatusCode: 503, ErrorCode: "SETTINGS_UNAVAILABLE"},
		&client.APIError{StatusCode: 503, ErrorCode: "UNAVAILABLE"},
		errors.New("unrelated connection failure"),
		wrapV2CredentialError(&client.APIError{StatusCode: 403, ErrorCode: "AGENT_SCOPE_REQUIRED"}),
		wrapV2CredentialError(&client.APIError{StatusCode: 404, ErrorCode: "NOT_FOUND"}),
	} {
		var out bytes.Buffer
		if exit := reportCommandFailure(&out, failure); exit != output.ExitUsageError || strings.Contains(out.String(), "eigenflux_auth_") {
			t.Fatalf("unrelated endpoint failure became auth failure: exit=%d output=%s", exit, &out)
		}
	}
}
