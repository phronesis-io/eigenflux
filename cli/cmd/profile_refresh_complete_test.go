package cmd

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/profilestate"
	"github.com/spf13/cobra"
)

func TestProfileRefreshCompleteSendsEvaluatedVersionHeader(t *testing.T) {
	for _, tc := range []struct {
		name      string
		expected  string
		completed bool
	}{
		{"current version", "7", true},
		{"stale version", "6", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/api/v2/agents/me/card/refresh-context" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				got = append(got, r.Header.Get(profileRefreshCompleteHeader))
				_, _ = w.Write([]byte(`{"code":0,"data":{"profile_version":7}}`))
			}))
			t.Cleanup(server.Close)
			_, serverName := runtimeTestConfig(t, server.URL, true)

			command := &cobra.Command{}
			command.Flags().Int64("expected-version", 0, "")
			if err := command.Flags().Set("expected-version", tc.expected); err != nil {
				t.Fatal(err)
			}
			err := profileRefreshCompleteCmd.RunE(command, nil)
			if (err == nil) != tc.completed {
				t.Fatalf("refresh-complete err = %v, want completed=%v", err, tc.completed)
			}
			if len(got) != 1 || got[0] != tc.expected {
				t.Fatalf("%s header = %q, want one request with %q", profileRefreshCompleteHeader, got, tc.expected)
			}
			state := profilestate.Load(config.HomeDir(), serverName, "agent-1")
			if (state.LastCheckedUnix > 0) != tc.completed {
				t.Fatalf("local completion stamp = %+v, want completed=%v", state, tc.completed)
			}
		})
	}
}
