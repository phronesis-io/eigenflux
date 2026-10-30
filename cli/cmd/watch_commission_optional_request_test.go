package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"cli.eigenflux.ai/internal/dispatch"
)

func TestCommissionOptionalRequestFileReachesIntakeAndFulfillment(t *testing.T) {
	for _, kind := range []string{"commission_order", "commission_fulfillment"} {
		t.Run(kind, func(t *testing.T) {
			content := `{"input":"今天也想见你","count":10,"level":"标准","style":"混搭"}`
			digest := sha256.Sum256([]byte(content))
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v2/console/trade/orders/901":
					entry := materialTestEntry()
					entry["byte_size"], entry["sha256"] = len(content), hex.EncodeToString(digest[:])
					materialTestResponse(w, materialTestDetail([]map[string]any{entry}))
				case "/api/v1/orders/901/snapshots/9007199254740995/download":
					materialTestResponse(w, map[string]any{"grant": map[string]any{"method": "GET", "url": server.URL + "/blob"}})
				case "/blob":
					_, _ = io.WriteString(w, content)
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			w := materialTestWatch(t, server.URL)
			installCommissionIntakeRules(t, w)
			order := materialTestOrder()
			order.BuyerInput = ""
			order.Contract = json.RawMessage(`{"fulfillment_skill":"input-review","requires_materials":false}`)
			prompt, required, files, cleanup, err := w.commissionAgentPrompt(context.Background(), dispatch.Job{ID: "request", Kind: kind}, order, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			if required || len(files) != 1 || !strings.Contains(prompt.Prompt, `"logical_path":"inputs/request.txt"`) {
				t.Fatalf("optional text request omitted: required=%v files=%+v", required, files)
			}
			actual, err := os.ReadFile(files[0].Path)
			if err != nil || string(actual) != content {
				t.Fatalf("request bytes lost: %s %v", actual, err)
			}
			ready := dispatch.CommissionIntakeDecision{Outcome: "ready", InspectedFiles: []string{}}
			if validCommissionInspectedFiles(ready, files, required) {
				t.Fatal("accepted ready without inspecting optional request")
			}
			ready.InspectedFiles = []string{"inputs/request.txt"}
			if !validCommissionInspectedFiles(ready, files, required) {
				t.Fatal("rejected inspected request")
			}
		})
	}
}
