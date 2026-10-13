package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func resetPublishFlagsForTest(t *testing.T) {
	t.Helper()
	reset := func() {
		originFlag = ""
		if flag := rootCmd.PersistentFlags().Lookup("origin"); flag != nil {
			flag.Changed = false
		}
		for _, name := range []string{"content", "notes", "url"} {
			_ = publishCmd.Flags().Set(name, "")
			publishCmd.Flags().Lookup(name).Changed = false
		}
	}
	reset()
	t.Cleanup(reset)
}

func publishOriginTestServer(t *testing.T, bodies *[]map[string]any) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/items/publish") {
			// Unrelated background reports are answered but not recorded.
			_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{}}`))
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		*bodies = append(*bodies, body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{"item_id":"201"}}`))
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func TestPublishSendsOriginOnlyWhenGiven(t *testing.T) {
	notes := `{"type":"info","domains":["tech"],"summary":"s","expire_time":"2030-01-01T00:00:00Z","source_type":"original"}`
	cases := []struct {
		name string
		args []string
		want any
	}{
		{"absent", []string{"publish", "--content", "c", "--notes", notes}, nil},
		{"owner on publish", []string{"publish", "--origin", "owner", "--content", "c", "--notes", notes}, "owner"},
		{"heartbeat from cycle prefix", []string{"--origin", "heartbeat", "publish", "--content", "c", "--notes", notes}, "heartbeat"},
		{"explicit owner after heartbeat prefix", []string{"--origin", "heartbeat", "publish", "--origin", "owner", "--content", "c", "--notes", notes}, "owner"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var bodies []map[string]any
			runtimeTestConfig(t, publishOriginTestServer(t, &bodies), false)
			resetPublishFlagsForTest(t)
			if _, err := runArgs(t, tc.args...); err != nil {
				t.Fatal(err)
			}
			if len(bodies) != 1 {
				t.Fatalf("publish requests=%d", len(bodies))
			}
			got, present := bodies[0]["publish_origin"]
			if tc.want == nil {
				if present {
					t.Fatalf("publish_origin must be omitted when no origin is given, body=%v", bodies[0])
				}
			} else if got != tc.want {
				t.Fatalf("publish_origin=%v, want %v", got, tc.want)
			}
			if bodies[0]["content"] != "c" || bodies[0]["notes"] != notes || bodies[0]["accept_reply"] != true {
				t.Fatalf("publish body changed: %v", bodies[0])
			}
		})
	}
}

func TestOriginFlagRejectsUnknownValuesBeforeAnyRequest(t *testing.T) {
	for _, value := range []string{"", "Heartbeat", "scheduled", "owner "} {
		t.Run(value, func(t *testing.T) {
			var bodies []map[string]any
			runtimeTestConfig(t, publishOriginTestServer(t, &bodies), false)
			resetPublishFlagsForTest(t)
			_, err := runArgs(t, "publish", "--origin", value, "--content", "c", "--notes", "{}")
			if err == nil || !strings.Contains(err.Error(), "--origin must be heartbeat or owner") {
				t.Fatalf("err=%v", err)
			}
			if len(bodies) != 0 {
				t.Fatalf("invalid --origin must not publish, got %d requests", len(bodies))
			}
		})
	}
}
