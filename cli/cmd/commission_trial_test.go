package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/commissionapi"
	"cli.eigenflux.ai/internal/dispatch"
)

func TestCommissionPublishRequiresRealTrial(t *testing.T) {
	command := commissionPublishCommand()
	if err := command.RunE(command, []string{"901"}); err == nil {
		t.Fatal("missing version accepted")
	}
	_ = command.Flags().Set("expected-version", "3")
	if err := command.RunE(command, []string{"901"}); err == nil || !strings.Contains(err.Error(), "--trial-input") {
		t.Fatalf("trial bypass: %v", err)
	}
}

func TestCommissionPublishTrialGatesMutation(t *testing.T) {
	for _, scenario := range []string{"ready", "needs_user", "needs_input", "failed", "run_error", "invalid_json", "missing_artifact", "empty_artifact", "wrong_request", "changed_draft", "changed_skill", "changed_identity", "materials_required", "wrong_seller"} {
		t.Run(scenario, func(t *testing.T) {
			ran, published := false, false
			draft := commissionTrialDraft{Draft: commissionapi.CommissionInput{FulfillmentSkill: "input-review", DeliverySpecText: "report.txt containing a checked result"}, Version: 3}
			draft.Definition.ID, draft.Definition.Seller = 901, 42
			if scenario == "materials_required" {
				draft.Draft.RequiresMaterials = true
			}
			if scenario == "wrong_seller" {
				draft.Definition.Seller = 43
			}
			server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/commissions/901" && r.Method == http.MethodGet {
					current := draft
					if ran && scenario == "changed_draft" {
						current.Version++
					}
					materialTestResponse(out, map[string]any{"commission": current})
					return
				}
				if r.URL.Path == "/api/v1/commissions/901/publish" && r.Method == http.MethodPost {
					if !ran || scenario != "ready" {
						t.Errorf("published before valid trial: %s", scenario)
					}
					var body map[string]int64
					_ = json.NewDecoder(r.Body).Decode(&body)
					if body["expected_draft_version"] != 3 {
						t.Error("wrong publish version")
					}
					published = true
					materialTestResponse(out, map[string]any{})
					return
				}
				t.Errorf("trial made unexpected request: %s %s", r.Method, r.URL.Path)
				http.Error(out, "unexpected", 500)
			}))
			defer server.Close()
			w := newCommissionWatch(t, server.URL, "commission_order")
			installCommissionIntakeRules(t, w)
			w.runAgent = func(ctx context.Context, req dispatch.Request) (dispatch.Result, error) {
				assertCommissionSkillReadScope(t, req, filepath.Join(w.binding.SkillsDir, "input-review"))
				ran = true
				if req.Kind != "commission_publish_trial" || req.SessionID != "" {
					t.Error("wrong runner lane/session")
				}
				_, raw, ok := strings.Cut(req.Prompt, "UNTRUSTED TRIAL DATA:\n")
				if !ok {
					t.Fatal("missing trial payload")
				}
				var payload struct {
					Output string `json:"output_directory"`
					Input  string `json:"buyer_input"`
				}
				if json.Unmarshal([]byte(raw), &payload) != nil || payload.Input != "representative request" {
					t.Fatal("bad sample")
				}
				decision := dispatch.CommissionTrialDecision{Version: 1, RequestID: req.ID, CommissionID: "901", DraftVersion: 3, Outcome: "artifacts_ready", Summary: "Generated report", SelfCheck: "Read bytes and verified report contents", Artifacts: []dispatch.CommissionArtifact{{LogicalPath: "report.txt", RelativePath: "report.txt"}}}
				switch scenario {
				case "needs_user", "needs_input", "failed":
					decision.Outcome = scenario
					decision.Artifacts = []dispatch.CommissionArtifact{}
				case "run_error":
					return dispatch.Result{}, errors.New("timeout")
				case "invalid_json":
					return dispatch.Result{Text: "ready"}, nil
				case "wrong_request":
					decision.RequestID = "wrong"
				case "changed_skill":
					_ = os.WriteFile(filepath.Join(w.binding.SkillsDir, "input-review", "SKILL.md"), []byte("---\nname: input-review\n---\nChanged"), 0600)
				case "changed_identity":
					cred, _ := auth.LoadV2Credentials(w.server.Name)
					cred.AgentID = "43"
					if err := auth.SaveV2Credentials(w.server.Name, cred); err != nil {
						t.Fatal(err)
					}
				}
				if scenario != "missing_artifact" {
					content := []byte("Checked report")
					if scenario == "empty_artifact" {
						content = nil
					}
					if err := os.WriteFile(filepath.Join(payload.Output, "report.txt"), content, 0600); err != nil {
						t.Fatal(err)
					}
				}
				encoded, _ := json.Marshal(decision)
				return dispatch.Result{Text: string(encoded)}, nil
			}
			command := commissionPublishCommand()
			command.SetContext(context.Background())
			command.SetErr(io.Discard)
			err := w.publishWithTrial(command, 901, 3, []byte("representative request"))
			if scenario == "ready" {
				if err != nil || !published {
					t.Fatalf("valid trial rejected: %v", err)
				}
			} else if err == nil || published {
				t.Fatalf("invalid trial published: %v %v", err, published)
			}
			if (scenario == "wrong_seller" || scenario == "materials_required") && ran {
				t.Fatal("Agent ran despite failed preflight")
			}
		})
	}
}

func TestCommissionTrialMaterialsIsolation(t *testing.T) {
	source := t.TempDir()
	original := filepath.Join(source, "input.txt")
	_ = os.WriteFile(original, []byte("sample"), 0600)
	destination := filepath.Join(t.TempDir(), "input")
	files, err := prepareCommissionTrialMaterials(source, destination)
	if err != nil || len(files) != 1 || files[0].Path == original {
		t.Fatalf("not isolated: %+v %v", files, err)
	}
	_ = os.WriteFile(original, []byte("changed"), 0600)
	copied, _ := os.ReadFile(files[0].Path)
	if string(copied) != "sample" {
		t.Fatal("sample changed through source")
	}
	if err = os.Symlink(original, filepath.Join(source, "link")); err != nil {
		t.Skip(err)
	}
	if _, err = prepareCommissionTrialMaterials(source, filepath.Join(t.TempDir(), "input")); err == nil {
		t.Fatal("symlink accepted")
	}
}

func TestCommissionTrialRealRunner(t *testing.T) {
	if os.Getenv("EF_TRIAL_HELPER") == "1" {
		raw, _ := io.ReadAll(os.Stdin)
		var data struct {
			Directory string `json:"output_directory"`
		}
		if json.Unmarshal(raw, &data) != nil {
			os.Exit(2)
		}
		if os.WriteFile(filepath.Join(data.Directory, "report.txt"), []byte("actual output"), 0600) != nil {
			os.Exit(3)
		}
		_ = json.NewEncoder(os.Stdout).Encode(dispatch.CommissionTrialDecision{Version: 1, RequestID: "trial", CommissionID: "901", DraftVersion: 3, Outcome: "artifacts_ready", Summary: "Created output", SelfCheck: "Read output", Artifacts: []dispatch.CommissionArtifact{{LogicalPath: "report.txt", RelativePath: "report.txt"}}})
		os.Exit(0)
	}
	directory := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	runner := dispatch.Runner{Binding: dispatch.Binding{Mode: "command", Command: []string{executable, "-test.run=^TestCommissionTrialRealRunner$"}, WorkDir: directory, Home: directory, TimeoutSeconds: 10, Env: map[string]string{"EF_TRIAL_HELPER": "1"}}}
	payload, _ := json.Marshal(map[string]string{"output_directory": directory})
	result, err := runner.Run(context.Background(), dispatch.Request{ID: "trial", Kind: "commission_publish_trial", Prompt: string(payload)})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := dispatch.ParseCommissionTrialDecision(result.Text, "trial", "901", 3)
	if err != nil {
		t.Fatal(err)
	}
	artifacts, err := validateCommissionArtifacts(directory, decision)
	if err != nil || len(artifacts) != 1 {
		t.Fatalf("invalid subprocess output: %v", err)
	}
}
