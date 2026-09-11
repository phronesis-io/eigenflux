package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/config"
)

func TestMsgTopicStatusValidatesRequiredValues(t *testing.T) {
	t.Cleanup(func() {
		_ = msgTopicStatusCmd.Flags().Set("conv-id", "")
		_ = msgTopicStatusCmd.Flags().Set("status", "")
	})

	_ = msgTopicStatusCmd.Flags().Set("conv-id", "")
	_ = msgTopicStatusCmd.Flags().Set("status", "open")
	if err := msgTopicStatusCmd.RunE(msgTopicStatusCmd, nil); err == nil || !strings.Contains(err.Error(), "--conv-id is required") {
		t.Fatalf("missing conv-id error=%v", err)
	}

	_ = msgTopicStatusCmd.Flags().Set("conv-id", "123")
	_ = msgTopicStatusCmd.Flags().Set("status", "waiting")
	if err := msgTopicStatusCmd.RunE(msgTopicStatusCmd, nil); err == nil || !strings.Contains(err.Error(), "--status must be") {
		t.Fatalf("invalid status error=%v", err)
	}
}

func TestMsgTopicStatusUsesAuthenticatedAPIRoute(t *testing.T) {
	for _, tc := range []struct {
		name string
		v2   bool
		path string
	}{
		{name: "legacy", path: "/api/v1/pm/topic-status"},
		{name: "agent v2", v2: true, path: "/api/v2/pm/conversations/topic-status"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tempHome(t)
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != http.MethodPost || r.URL.Path != tc.path {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
					return
				}
				if got := r.Header.Get("Authorization"); got != "Bearer topic-token" {
					t.Errorf("authorization=%q", got)
				}
				var body map[string]string
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body["conv_id"] != "123" || body["topic_status"] != "pending_verify" {
					t.Errorf("topic request body=%v", body)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{"conv_id":"123","topic_status":"pending_verify","changed":true}}`))
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
			if tc.v2 {
				err = auth.SaveV2Credentials(active.Name, &auth.V2Credentials{
					AgentID: "42", PrincipalID: "24", AccessToken: "topic-token", RefreshToken: "topic-refresh",
					ExpiresAt: time.Now().Add(time.Hour).UnixMilli(),
				})
			} else {
				err = auth.SaveCredentials(active.Name, &auth.Credentials{AgentID: "42", AccessToken: "topic-token"})
			}
			if err != nil {
				t.Fatal(err)
			}
			previousServer := serverFlag
			serverFlag = ""
			t.Cleanup(func() {
				serverFlag = previousServer
				_ = msgTopicStatusCmd.Flags().Set("conv-id", "")
				_ = msgTopicStatusCmd.Flags().Set("status", "")
			})
			_ = msgTopicStatusCmd.Flags().Set("conv-id", "123")
			_ = msgTopicStatusCmd.Flags().Set("status", "pending_verify")
			if err := msgTopicStatusCmd.RunE(msgTopicStatusCmd, nil); err != nil {
				t.Fatalf("topic-status command failed: %v", err)
			}
			if requests != 1 {
				t.Fatalf("requests=%d, want one topic-status write", requests)
			}
		})
	}
}
