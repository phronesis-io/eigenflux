package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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
