package cmd

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/config"
)

func TestFeedV2PollOptions(t *testing.T) {
	for _, tc := range []struct{ action, cursor, wantError string }{
		{"", "", ""}, {"refresh", "", ""}, {"more", "", ""},
		{"", "123", "--cursor"}, {"more", "123", "--cursor"},
		{"refresh", "123", "--cursor"}, {"invalid", "", "--action"},
	} {
		t.Run(tc.action+"/"+tc.cursor, func(t *testing.T) {
			calls := 0
			err := runFeedV2Poll(tc.action, tc.cursor, func() error { calls++; return nil })
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) || calls != 0 {
					t.Fatalf("err=%v calls=%d", err, calls)
				}
			} else if err != nil || calls != 1 {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
		})
	}
}

func TestFeedV2PollPreservesErrors(t *testing.T) {
	for _, status := range []int{401, 403, 404, 503} {
		want := &client.APIError{StatusCode: status}
		calls := 0
		err := runFeedV2Poll("more", "", func() error { calls++; return want })
		if !errors.Is(err, want) || calls != 1 {
			t.Fatalf("status=%d err=%v calls=%d", status, err, calls)
		}
	}
}

func TestTopicStatusRoutesByCredentials(t *testing.T) {
	for _, v2 := range []bool{false, true} {
		name := "v1"
		wantPath := "/api/v1/pm/topic-status"
		if v2 {
			name = "v2"
			wantPath = "/api/v2/pm/conversations/topic-status"
		}
		t.Run(name, func(t *testing.T) {
			tempHome(t)
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodPost || r.URL.Path != wantPath {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				var body map[string]string
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["conv_id"] != "456" || body["topic_status"] != "open" {
					t.Errorf("unexpected body %v: %v", body, err)
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 0, "data": map[string]interface{}{}})
			}))
			defer server.Close()
			cfg, err := config.Load()
			if err != nil {
				t.Fatal(err)
			}
			active, err := cfg.GetActive("")
			if err != nil {
				t.Fatal(err)
			}
			if err := cfg.UpdateServer(active.Name, server.URL, ""); err != nil {
				t.Fatal(err)
			}
			if v2 {
				err = auth.SaveV2Credentials(active.Name, &auth.V2Credentials{AgentID: "42", PrincipalID: "24", AccessToken: "v2-token", RefreshToken: "refresh", ExpiresAt: time.Now().Add(time.Hour).UnixMilli()})
			} else {
				err = auth.SaveCredentials(active.Name, &auth.Credentials{AgentID: "42", AccessToken: "v1-token"})
			}
			if err != nil {
				t.Fatal(err)
			}
			for flag, value := range map[string]string{"conv-id": "456", "status": "open"} {
				old := msgTopicStatusCmd.Flags().Lookup(flag).Value.String()
				if err := msgTopicStatusCmd.Flags().Set(flag, value); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = msgTopicStatusCmd.Flags().Set(flag, old) })
			}
			if err := msgTopicStatusCmd.RunE(msgTopicStatusCmd, nil); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("calls=%d", calls)
			}
		})
	}
}
