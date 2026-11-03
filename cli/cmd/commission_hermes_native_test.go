package cmd

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/commissionapi"
	"cli.eigenflux.ai/internal/dispatch"
)

// Explicit opt-in: use the operator's configured Hermes model and file tools.
// Every EigenFlux identity and API is an isolated loopback fixture; no service
// is actually published, and no real Order or payment is created.
func TestCommissionHermesLocalPublishTrial(t *testing.T) {
	if os.Getenv("EIGENFLUX_HERMES_PUBLISH_SMOKE") != "1" {
		t.Skip("set EIGENFLUX_HERMES_PUBLISH_SMOKE=1 for real local Hermes model usage")
	}
	executable := os.Getenv("EIGENFLUX_HERMES_SMOKE_EXECUTABLE")
	if executable == "" {
		var err error
		executable, err = exec.LookPath("hermes")
		if err != nil {
			t.Fatal(err)
		}
	}
	if !filepath.IsAbs(executable) {
		t.Fatal("Hermes smoke executable must be an absolute path")
	}
	draft := commissionTrialDraft{Draft: commissionapi.CommissionInput{
		Title: "Local Hermes fixture", FulfillmentSkill: "input-review", RequestSpecSchema: "{}",
		DeliverySpecText: "Create exactly one UTF-8 plain text file at outputs/report.txt, containing exactly hermes-publish-trial:42 followed by one LF newline. Use relative_path report.txt inside output_directory. Read back the bytes before returning the required JSON decision.",
	}}
	draft.Definition.ID, draft.Definition.Seller, draft.Version = 901, 42, 3
	var published atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/api/v1/commissions/901":
			materialTestResponse(out, map[string]any{"commission": draft})
		case request.Method == http.MethodPost && request.URL.Path == "/api/v1/commissions/901/publish":
			var body map[string]int64
			if json.NewDecoder(request.Body).Decode(&body) != nil || body["expected_draft_version"] != 3 {
				t.Error("wrong fixture publication version")
			}
			published.Add(1)
			materialTestResponse(out, map[string]any{})
		default:
			t.Errorf("unexpected fixture request: %s %s", request.Method, request.URL.Path)
			http.NotFound(out, request)
		}
	}))
	defer server.Close()
	w := newCommissionWatch(t, server.URL, "commission_order")
	installCommissionIntakeRules(t, w)
	w.binding.Mode, w.binding.Host = "native", "hermes"
	w.binding.Command, w.binding.Args, w.binding.TimeoutSeconds = []string{executable}, nil, 600
	if err := dispatch.WriteJSON(dispatch.BindingPath(w.home, w.server.Name), w.binding); err != nil {
		t.Fatal(err)
	}
	runner := dispatch.Runner{Binding: *w.binding}
	w.runAgent = runner.Run
	command := commissionPublishCommand()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	command.SetContext(ctx)
	command.SetErr(io.Discard)
	started := time.Now()
	if err := w.publishWithTrial(command, 901, 3, []byte("Generate and inspect the exact contracted local file.")); err != nil {
		t.Fatalf("real Hermes publication trial failed: %v", err)
	}
	if published.Load() != 1 {
		t.Fatalf("expected one fixture publish after trial, got %d", published.Load())
	}
	evidencePaths, err := filepath.Glob(filepath.Join(w.binding.WorkDir, ".eigenflux-publish-trial-*", "evidence.json"))
	if err != nil || len(evidencePaths) != 1 {
		t.Fatalf("missing verified trial evidence: %v", err)
	}
	artifactPath := filepath.Join(filepath.Dir(evidencePaths[0]), "output", "report.txt")
	content, err := os.ReadFile(artifactPath)
	if err != nil || string(content) != "hermes-publish-trial:42\n" {
		t.Fatal("real Hermes artifact failed the exact-byte contract")
	}
	t.Logf("real Hermes publication trial passed in %.3fs; artifact_bytes=%d; fixture_publish_calls=%d; no live service, Order or payment", time.Since(started).Seconds(), len(content), published.Load())
}
