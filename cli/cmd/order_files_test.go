package cmd

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestOrderFilesUsesCommissionManifest(t *testing.T) {
	for _, scenario := range []string{"files", "empty", "forbidden"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			materialTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "GET" || r.URL.Path != "/api/v1/orders/373453161391718400/files" || r.Header.Get("Authorization") != "Bearer test-token" {
					t.Errorf("unexpected manifest request %s %s", r.Method, r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				switch scenario {
				case "forbidden":
					w.WriteHeader(http.StatusForbidden)
					_, _ = io.WriteString(w, `{"code":403,"msg":"not a participant"}`)
				case "empty":
					_, _ = io.WriteString(w, `{"code":0,"data":{"input_files":[],"output_files":[]}}`)
				default:
					_, _ = io.WriteString(w, `{"code":0,"data":{"input_files":[{"logical_path":"inputs/request.txt","snapshot_id":373453165057540096}],"output_files":[{"logical_path":"outputs/tuwei-lines.md","snapshot_id":373465283400040448}]}}`)
				}
			})
			out, err := captureHeartbeatStdout(t, func() error { return orderFilesCmd.RunE(orderFilesCmd, []string{"373453161391718400"}) })
			if calls != 1 || (err != nil) != (scenario == "forbidden") {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
			if scenario == "files" && (!strings.Contains(out, "inputs/request.txt") || !strings.Contains(out, "373465283400040448")) {
				t.Fatalf("manifest paths or exact IDs missing: %s", out)
			}
			if scenario == "empty" && (!strings.Contains(out, `"input_files": []`) || !strings.Contains(out, `"output_files": []`)) {
				t.Fatalf("empty manifest must remain explicit: %s", out)
			}
		})
	}
}
