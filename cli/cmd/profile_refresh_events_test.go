package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/profilestate"
	"github.com/spf13/cobra"
	"reflect"
)

func TestProfileFunnelFailureReasons(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{nil, ""},
		{errors.New("private text at /Users/private?token=secret"), "client_other"},
		{&client.APIError{StatusCode: 409}, "version_conflict"},
		{errors.New("profile changed since version 1"), "version_conflict"},
		{&client.APIError{StatusCode: 401}, "api_unauthorized"},
		{&client.APIError{StatusCode: 403}, "api_forbidden"},
		{&client.APIError{StatusCode: 404}, "api_not_found"},
		{&client.APIError{StatusCode: 429}, "api_rate_limited"},
		{&client.APIError{StatusCode: 500}, "api_server"},
		{&client.APIError{StatusCode: 400, ErrorCode: "private-token"}, "api_other"},
		{&client.APIError{StatusCode: 503, ErrorCode: "FEED_CONTEXT_READ_FAILED"}, "api_feed_context_read_failed"},
		{fmt.Errorf("wrapped: %w", context.DeadlineExceeded), "transport_timeout"},
		{&url.Error{Op: "Get", URL: "https://private?token=secret", Err: &net.DNSError{Err: "private host"}}, "transport_network"},
		{fmt.Errorf("parse response: %w", &json.SyntaxError{Offset: 1}), "response_decode"},
		{&os.PathError{Op: "read", Path: "/Users/private", Err: os.ErrPermission}, "local_storage"},
		{fmt.Errorf("wrapped: %w", feedDiagnostic("baseline_formal_context", errors.New("baseline Feed V2 must not contain formal control context"))), "baseline_formal_context"},
	} {
		if got := profileFailureReason(tc.err); got != tc.want {
			t.Errorf("reason=%q want=%q", got, tc.want)
		}
	}
}

func TestFeedV2ValidationFailureReasons(t *testing.T) {
	t.Setenv("EIGENFLUX_HOME", t.TempDir())
	for _, tc := range []struct{ payload, want string }{
		{`{"schema_version":"wrong"}`, "feed_schema_invalid"},
		{`{"schema_version":"feed.v2","personalization":{"mode":"baseline","context_revision":1}}`, "baseline_formal_context"},
		{`{"schema_version":"feed.v2","personalization":{"mode":"baseline"},"control_context_snapshot":{}}`, "baseline_formal_context"},
		{`{"schema_version":"feed.v2","personalization":{"mode":"unknown"}}`, "personalization_invalid"},
		{`{"schema_version":"feed.v2","personalization":{"mode":"intent_aligned","context_revision":1},"control_context_snapshot":{"context_revision":2}}`, "context_snapshot_mismatch"},
		{`{"schema_version":"feed.v2","personalization":{"mode":"intent_aligned","context_revision":1}}`, "context_snapshot_unavailable"},
	} {
		_, _, err := hydrateFeedV2ControlContext("test-server", "owner", json.RawMessage(tc.payload))
		if err == nil || profileFailureReason(err) != tc.want || profileFailureCode(err) != "client_error" {
			t.Errorf("payload=%s error=%v reason=%q", tc.payload, err, profileFailureReason(err))
		}
	}
}

func TestProfileFunnelFailureReasonSentWithoutPrivateText(t *testing.T) {
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var received profileFunnelEvent
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Error(err)
		}
		encoded, _ := json.Marshal(received)
		body = string(encoded)
		if received.FailureCode != "client_error" || received.FailureReason != "transport_network" {
			t.Errorf("event=%+v", received)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	runtimeTestConfig(t, server.URL, true)
	err := &url.Error{Op: "Get", URL: "https://private?token=secret", Err: &net.DNSError{Err: "private host"}}
	reportProfileFunnel(activeServerName(), profileFunnelEvent{Stage: "poll_failed", PollID: newProfileRunID(), Cause: "feed_poll", FailureCode: profileFailureCode(err), FailureReason: profileFailureReason(err)})
	if body == "" || strings.Contains(body, "private") || strings.Contains(body, "secret") {
		t.Fatalf("unsafe/missing event %q", body)
	}
}

func TestProfileFunnelFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{nil, ""}, {errors.New("private text"), "client_error"},
		{&client.APIError{StatusCode: 409}, "version_conflict"},
		{&client.APIError{StatusCode: 500}, "api_error"},
		{errors.New("profile changed since version 1"), "version_conflict"},
	} {
		if got := profileFailureCode(tc.err); got != tc.want {
			t.Fatalf("got %q want %q", got, tc.want)
		}
	}
}
func TestProfileFunnelRunPreservedUntilExpiry(t *testing.T) {
	s := profilestate.State{}
	now := time.Now().Unix()
	recordProfileRefreshDispatch(&s, "scheduled", now, time.Hour)
	first := s.RefreshRunID
	if len(first) != 32 {
		t.Fatalf("run id %q", first)
	}
	settleProfileRefreshTrigger(&s, now)
	if s.RefreshRunID != first {
		t.Fatal("completion lost correlation")
	}
	recordProfileRefreshDispatch(&s, "manual", now, time.Hour)
	if s.RefreshRunID == first || s.RefreshCause != "manual" {
		t.Fatal("new dispatch must own a new run")
	}
}
func TestProfileFunnelHTTPObservation(t *testing.T) {
	var received profileFunnelEvent
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/agents/me/card/refresh-events" {
			t.Errorf("path %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	runtimeTestConfig(t, server.URL, true)
	reportProfileFunnel(activeServerName(), profileFunnelEvent{Stage: "poll_started", PollID: newProfileRunID(), Cause: "feed_poll"})
	if received.Stage != "poll_started" {
		t.Fatal("observation not sent")
	}
}

func TestProfileContextFailureReportsAttempt(t *testing.T) {
	var event profileFunnelEvent
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_ = json.NewDecoder(r.Body).Decode(&event)
			_, _ = w.Write([]byte(`{"code":0}`))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	_, name := runtimeTestConfig(t, server.URL, true)
	state := profilestate.State{}
	recordProfileRefreshDispatch(&state, "scheduled", time.Now().Unix(), time.Hour)
	if err := profilestate.Save(config.HomeDir(), name, "agent-1", state); err != nil {
		t.Fatal(err)
	}
	if err := profileRefreshContextCmd.RunE(&cobra.Command{}, nil); err == nil {
		t.Fatal("failed context unexpectedly succeeded")
	}
	if event.Stage != "refresh_attempt_failed" || event.RunID != state.RefreshRunID || event.FailureCode != "api_error" {
		t.Fatalf("attempt not reported: %+v", event)
	}
}
func TestProfileInputFailureReportsAttempt(t *testing.T) {
	run, name := newRefreshRunFixture(t)
	state := profilestate.State{}
	recordProfileRefreshDispatch(&state, "scheduled", time.Now().Unix(), time.Hour)
	if err := profilestate.Save(config.HomeDir(), name, "agent-1", state); err != nil {
		t.Fatal(err)
	}
	command := &cobra.Command{}
	command.Flags().String("file", "", "")
	command.Flags().String("source", "cli_daily_refresh", "")
	command.Flags().String("reason", "", "")
	command.Flags().Int64("expected-version", 0, "")
	if err := profilePatchCmd.RunE(command, nil); err == nil {
		t.Fatal("invalid patch accepted")
	}
	if len(run.dispatches) != 1 || run.dispatches[0].Stage != "refresh_attempt_failed" || run.dispatches[0].RunID != state.RefreshRunID {
		t.Fatalf("input failure not observed: %+v", run.dispatches)
	}
}

func TestProfileFunnelUsesOnlyUsableCachedAccess(t *testing.T) {
	for _, tc := range []struct {
		name   string
		v2     bool
		expiry int64
		want   int
	}{
		{"expired V2", true, 1, 0}, {"unknown V2 expiry", true, 0, 0},
		{"valid V2", true, time.Now().Add(time.Hour).UnixMilli(), 1},
		{"expired V1", false, 1, 0}, {"invalid negative V1 expiry", false, -1, 0}, {"unknown legacy expiry", false, 0, 1},
		{"valid V1", false, time.Now().Add(time.Hour).UnixMilli(), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.URL.Path != "/api/v1/agents/me/card/refresh-events" && r.URL.Path != "/api/v2/agents/me/card/refresh-events" {
					t.Errorf("unexpected authentication request: %s", r.URL.Path)
				}
				w.WriteHeader(http.StatusUnauthorized)
			}))
			defer h.Close()
			_, server := runtimeTestConfig(t, h.URL, tc.v2)
			if tc.v2 {
				before, err := auth.LoadV2Credentials(server)
				if err != nil {
					t.Fatal(err)
				}
				before.ExpiresAt = tc.expiry
				if err := auth.SaveV2Credentials(server, before); err != nil {
					t.Fatal(err)
				}
				reportProfileFunnel(server, profileFunnelEvent{Stage: "poll_started", PollID: newProfileRunID()})
				after, err := auth.LoadV2Credentials(server)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(before, after) {
					t.Fatal("telemetry changed credentials")
				}
			} else {
				before, err := auth.LoadCredentials(server)
				if err != nil {
					t.Fatal(err)
				}
				before.ExpiresAt = tc.expiry
				if err := auth.SaveCredentials(server, before); err != nil {
					t.Fatal(err)
				}
				reportProfileFunnel(server, profileFunnelEvent{Stage: "poll_started", PollID: newProfileRunID()})
				after, err := auth.LoadCredentials(server)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(before, after) {
					t.Fatal("telemetry changed credentials")
				}
			}
			if requests != tc.want {
				t.Fatalf("requests=%d want %d", requests, tc.want)
			}
		})
	}
}
