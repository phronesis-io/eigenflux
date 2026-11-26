package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRelationApplyUsesServerGreetingLimit(t *testing.T) {
	for _, tc := range []struct {
		name      string
		v2        bool
		greeting  string
		limit     int
		status    int
		counting  string
		wantError string
	}{
		{"v1 exact mixed length", false, "猫A🙂", 4, 200, "cjk_2_other_1", ""},
		{"v2 lower server limit", true, "猫A🙂", 3, 200, "cjk_2_other_1", "uses 4 weighted characters; the server limit is 3"},
		{"v2 higher server limit", true, strings.Repeat("犬", 250), 500, 200, "cjk_2_other_1", ""},
		{"unknown counting rule", true, "cat", 3, 200, "unknown", "cannot verify --greeting length"},
		{"missing limit", true, "pet", 0, 200, "cjk_2_other_1", "cannot verify --greeting length"},
		{"unavailable policy", false, "cat", 0, 404, "", "cannot verify --greeting length"},
		{"empty greeting skips lookup", false, "", 0, 404, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prefix := "/api/v1/relations/"
			if tc.v2 {
				prefix = "/api/v2/relations/"
			}
			var calls []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, r.Method+" "+r.URL.Path)
				switch r.URL.Path {
				case prefix + "limits":
					w.WriteHeader(tc.status)
					fmt.Fprintf(w, `{"code":0,"data":{"greeting":{"max_weighted_characters":%d,"counting":%q}}}`, tc.limit, tc.counting)
				case prefix + "apply":
					var body map[string]string
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["greeting"] != tc.greeting || body["to_short_id"] != "AbCdE" {
						t.Errorf("sent body = %#v, error = %v", body, err)
					}
					fmt.Fprint(w, `{"code":0,"data":{"request_id":"1"}}`)
				default:
					t.Errorf("unexpected route %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			runtimeTestConfig(t, server.URL, tc.v2)
			flags := relationApplyCmd.Flags()
			for name, value := range map[string]string{"to-short-id": "AbCdE", "greeting": tc.greeting} {
				flag := flags.Lookup(name)
				oldValue, oldChanged := flag.Value.String(), flag.Changed
				t.Cleanup(func() {
					_ = flag.Value.Set(oldValue)
					flag.Changed = oldChanged
				})
				if err := flags.Set(name, value); err != nil {
					t.Fatal(err)
				}
			}
			err := relationApplyCmd.RunE(relationApplyCmd, nil)
			if (tc.wantError == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tc.wantError)) {
				t.Fatalf("error = %v, want %q", err, tc.wantError)
			}
			var wantCalls []string
			if tc.greeting != "" {
				wantCalls = append(wantCalls, "GET "+prefix+"limits")
			}
			if tc.wantError == "" {
				wantCalls = append(wantCalls, "POST "+prefix+"apply")
			}
			if strings.Join(calls, ",") != strings.Join(wantCalls, ",") {
				t.Fatalf("requests = %v, want %v", calls, wantCalls)
			}
		})
	}
}
