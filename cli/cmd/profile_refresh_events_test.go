package cmd

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/profilestate"
	"github.com/spf13/cobra"
	"reflect"
)

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
