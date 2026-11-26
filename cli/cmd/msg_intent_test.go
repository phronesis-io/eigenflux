package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/config"
)

func TestMessageIntentSendPayload(t *testing.T) {
	for _, tc := range []struct{ name, item, conv, kind, reply, want string }{
		{"new contact", "1", "", "", "", "contact"},
		{"existing unknown", "", "2", "", "", ""},
		{"explicit reply", "", "2", "", "3", "reply"},
		{"explicit contact", "", "2", "contact", "", "contact"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tempHome(t)
			var body map[string]string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/pm/send" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{"msg_id":"10","conv_id":"2"}}`))
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
			if err := auth.SaveCredentials(active.Name, &auth.Credentials{AgentID: "42", AccessToken: "fixture-token"}); err != nil {
				t.Fatal(err)
			}
			oldServer := serverFlag
			serverFlag = ""
			flags := map[string]string{"content": "hello", "item-id": tc.item, "conv-id": tc.conv, "receiver-id": "", "message-kind": tc.kind, "reply-to-msg-id": tc.reply, "quote-msg-id": ""}
			t.Cleanup(func() {
				serverFlag = oldServer
				for name := range flags {
					_ = msgSendCmd.Flags().Set(name, "")
				}
			})
			for name, value := range flags {
				_ = msgSendCmd.Flags().Set(name, value)
			}
			if err := msgSendCmd.RunE(msgSendCmd, nil); err != nil {
				t.Fatal(err)
			}
			if body["message_kind"] != tc.want || body["reply_to_message_id"] != tc.reply {
				t.Fatalf("wrong attribution payload: %v", body)
			}
		})
	}
}

func TestMessageIntentRejectsAmbiguousReply(t *testing.T) {
	for _, tc := range []struct{ item, conv, kind, reply string }{
		{"", "2", "reply", ""}, {"", "", "", "3"}, {"1", "2", "", "3"}, {"", "2", "contact", "3"}, {"", "2", "", "0"}, {"", "2", "", "bad"}, {"", "2", "wrong", ""},
	} {
		if _, err := messageSendIntent(tc.item, tc.conv, tc.kind, tc.reply); err == nil {
			t.Fatalf("invalid reply accepted: %+v", tc)
		}
	}
}
