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
	"path/filepath"
	"strings"
	"testing"

	"cli.eigenflux.ai/internal/commissionapi"
	"cli.eigenflux.ai/internal/dispatch"
	"cli.eigenflux.ai/internal/requestcontract"
)

const scenarioSchema = `{"type":"object","required":["scenario"],"properties":{"scenario":{"type":"string"}}}`
const scenarioInput = "  {\"scenario\":\"My alarm did not ring\"}\n"

func TestCreateOrderUploadsCanonicalJSONBytes(t *testing.T) {
	local := filepath.Join(t.TempDir(), "request.json")
	if err := os.WriteFile(local, []byte(scenarioInput), 0600); err != nil {
		t.Fatal(err)
	}
	puts, creates := 0, 0
	var server *httptest.Server
	server = materialTestServer(t, func(out http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "POST /api/v1/order-preparations":
			materialTestResponse(out, map[string]any{"preparation_id": 51, "contract": map[string]string{"request_spec_text": "Upload inputs/request.txt", "request_spec_schema": scenarioSchema}})
		case "POST /api/v1/order-preparations/51/uploads":
			var body struct {
				Logical string `json:"logical_path"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Logical != requestcontract.Path {
				t.Errorf("wrong upload path: %+v %v", body, err)
			}
			materialTestResponse(out, map[string]any{"grant": map[string]any{"object_id": 71, "method": "PUT", "url": server.URL + "/object"}})
		case "PUT /object":
			data, _ := io.ReadAll(r.Body)
			if string(data) != scenarioInput {
				t.Errorf("JSON bytes changed: %q", data)
			}
			puts++
		case "POST /api/v1/order-preparations/51/uploads/confirm":
			materialTestResponse(out, map[string]any{})
		case "POST /api/v1/orders":
			var body struct {
				Files []materialFile `json:"input_files"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Files) != 1 || body.Files[0].LogicalPath != requestcontract.Path {
				t.Errorf("wrong final manifest: %+v %v", body, err)
			}
			creates++
			materialTestResponse(out, map[string]any{"order": map[string]any{"order_id": 51, "state": "awaiting_seller", "version": 1}})
		default:
			t.Errorf("unexpected route %s %s", r.Method, r.URL.Path)
			out.WriteHeader(http.StatusNotFound)
		}
	})
	materialFlag(t, orderCreateCmd, "buyer-input-file", local)
	if err := orderCreateCmd.RunE(orderCreateCmd, []string{"77"}); err != nil {
		t.Fatal(err)
	}
	if puts != 1 || creates != 1 {
		t.Fatalf("puts=%d creates=%d", puts, creates)
	}
}

func TestCreateOrderChecksFrozenRequestBeforeUpload(t *testing.T) {
	for _, tc := range []struct {
		name, text, schema, logical, content string
		missingContract                      bool
		want                                 string
	}{
		{name: "published path conflict", text: "Upload inputs/request.json", schema: scenarioSchema, logical: "inputs/request.json", content: scenarioInput, want: "must use inputs/request.txt"},
		{name: "wrong material path", text: "Upload inputs/request.txt", schema: scenarioSchema, logical: "inputs/request.json", content: scenarioInput, want: "structured requests must use"},
		{name: "non JSON request", text: "Upload inputs/request.txt", schema: scenarioSchema, logical: requestcontract.Path, content: "ordinary text", want: "UTF-8 JSON object"},
		{name: "null request", text: "Upload inputs/request.txt", schema: scenarioSchema, logical: requestcontract.Path, content: "null", want: "UTF-8 JSON object"},
		{name: "missing frozen contract", missingContract: true, logical: requestcontract.Path, content: scenarioInput, want: "missing frozen request contract"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			materialTestServer(t, func(out http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodPost || r.URL.Path != "/api/v1/order-preparations" {
					t.Fatalf("invalid materials reached upload/finalization: %s %s", r.Method, r.URL.Path)
				}
				data := map[string]any{"preparation_id": 51}
				if !tc.missingContract {
					data["contract"] = map[string]string{"request_spec_text": tc.text, "request_spec_schema": tc.schema}
				}
				materialTestResponse(out, data)
			})
			local := filepath.Join(t.TempDir(), "request.json")
			if err := os.WriteFile(local, []byte(tc.content), 0600); err != nil {
				t.Fatal(err)
			}
			materialFlag(t, orderCreateCmd, "input-file", tc.logical+"="+local)
			err := orderCreateCmd.RunE(orderCreateCmd, []string{"77"})
			if err == nil || !strings.Contains(err.Error(), tc.want) || calls != 1 {
				t.Fatalf("calls=%d error=%v", calls, err)
			}
		})
	}
}

func TestRequestMaterialValidationPreservesAttachmentsAndOptionalFields(t *testing.T) {
	if err := validateOrderRequestMaterials("Upload inputs/request.txt; optional fields", `{"type":"object"}`, nil); err != nil {
		t.Fatal(err)
	}
	if err := validateOrderRequestMaterials("Upload any attachments", `{}`, []materialFile{{LogicalPath: "inputs/request.json"}}); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(t.TempDir(), "request.json")
	if err := os.WriteFile(local, []byte(scenarioInput), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := inspectMaterial(requestcontract.Path, local)
	if err != nil {
		t.Fatal(err)
	}
	if err = validateOrderRequestMaterials("Upload inputs/request.txt", scenarioSchema, []materialFile{file, {LogicalPath: "inputs/data.json"}, {LogicalPath: "inputs/request.json"}}); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(local, []byte(`{"scenario":"changed"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err = validateOrderRequestMaterials("Upload inputs/request.txt", scenarioSchema, []materialFile{file}); err == nil || !strings.Contains(err.Error(), "changed after material inspection") {
		t.Fatalf("changed bytes accepted: %v", err)
	}
}

func TestStructuredPublishTrialUsesCanonicalRequestAndRejectsMismatch(t *testing.T) {
	for _, scenario := range []string{"ready", "bad_contract", "undeclared_path", "bad_json", "null", "wrong_material_path", "conflicting_sample", "matching_sample", "legacy_attachment"} {
		t.Run(scenario, func(t *testing.T) {
			ran, published := false, false
			draft := commissionTrialDraft{Version: 3, Draft: commissionapi.CommissionInput{FulfillmentSkill: "input-review", RequestSpecText: "Upload a UTF-8 JSON object at inputs/request.txt", RequestSpecSchema: scenarioSchema, RequiresMaterials: true, DeliverySpecText: "report.txt"}}
			draft.Definition.ID, draft.Definition.Seller = 901, 42
			if scenario == "bad_contract" {
				draft.Draft.RequestSpecText = "Upload inputs/request.json"
			}
			if scenario == "undeclared_path" {
				draft.Draft.RequestSpecText = "Describe your scenario"
			}
			input := []byte(scenarioInput)
			if scenario == "bad_json" {
				input = []byte("scenario: my alarm failed")
			}
			if scenario == "null" {
				input = []byte("null")
			}
			server := materialTestServer(t, func(out http.ResponseWriter, r *http.Request) {
				switch r.Method + " " + r.URL.Path {
				case "GET /api/v1/commissions/901":
					materialTestResponse(out, map[string]any{"commission": draft})
				case "POST /api/v1/commissions/901/publish":
					published = true
					if !ran {
						t.Error("publish preceded Agent trial")
					}
					materialTestResponse(out, map[string]any{})
				default:
					t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
				}
			})
			w := newCommissionWatch(t, server.URL, "commission_order")
			installCommissionIntakeRules(t, w)
			w.runAgent = func(_ context.Context, req dispatch.Request) (dispatch.Result, error) {
				ran = true
				_, raw, ok := strings.Cut(req.Prompt, "UNTRUSTED TRIAL DATA:\n")
				if !ok {
					t.Fatal("no trial data")
				}
				var payload struct {
					Files  []commissionLocalFile `json:"local_files"`
					Output string                `json:"output_directory"`
				}
				if err := json.Unmarshal([]byte(raw), &payload); err != nil {
					t.Fatal(err)
				}
				var requestFile *commissionLocalFile
				for i := range payload.Files {
					if payload.Files[i].LogicalPath == requestcontract.Path {
						requestFile = &payload.Files[i]
					}
				}
				wantFiles := 1
				if scenario == "legacy_attachment" {
					wantFiles = 2
				}
				if len(payload.Files) != wantFiles || requestFile == nil {
					t.Fatalf("wrong trial manifest: %+v", payload.Files)
				}
				file := *requestFile
				data, err := os.ReadFile(file.Path)
				if err != nil || string(data) != scenarioInput {
					t.Fatalf("trial changed request: %q %v", data, err)
				}
				sum := sha256.Sum256(data)
				if file.ByteSize != int64(len(data)) || file.SHA256 != hex.EncodeToString(sum[:]) {
					t.Fatal("wrong request receipt")
				}
				if err = os.WriteFile(filepath.Join(payload.Output, "report.txt"), []byte("Verified report"), 0600); err != nil {
					t.Fatal(err)
				}
				decision := dispatch.CommissionTrialDecision{Version: 1, RequestID: req.ID, CommissionID: "901", DraftVersion: 3, Outcome: "artifacts_ready", Summary: "Report generated", SelfCheck: "Read report", Artifacts: []dispatch.CommissionArtifact{{LogicalPath: "report.txt", RelativePath: "report.txt"}}}
				encoded, _ := json.Marshal(decision)
				return dispatch.Result{Text: string(encoded)}, nil
			}
			command := commissionPublishCommand()
			command.SetContext(context.Background())
			command.SetErr(io.Discard)
			if scenario == "wrong_material_path" || scenario == "conflicting_sample" || scenario == "matching_sample" || scenario == "legacy_attachment" {
				directory := t.TempDir()
				logical := requestcontract.Path
				content := input
				if scenario == "wrong_material_path" {
					logical = "inputs/request.json"
				}
				if scenario == "conflicting_sample" {
					content = []byte(`{"scenario":"different input"}`)
				}
				target := filepath.Join(directory, filepath.FromSlash(logical))
				if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(target, content, 0600); err != nil {
					t.Fatal(err)
				}
				if scenario == "legacy_attachment" {
					if err := os.WriteFile(filepath.Join(directory, "inputs", "request.json"), []byte("legacy attachment"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if err := command.Flags().Set("trial-materials", directory); err != nil {
					t.Fatal(err)
				}
			}
			err := w.publishWithTrial(command, 901, 3, input)
			wantSuccess := scenario == "ready" || scenario == "matching_sample" || scenario == "legacy_attachment"
			if wantSuccess {
				if err != nil || !ran || !published {
					t.Fatalf("valid trial rejected: %v ran=%t published=%t", err, ran, published)
				}
			} else if err == nil || ran || published {
				t.Fatalf("inconsistent trial passed: %v ran=%t published=%t", err, ran, published)
			}
		})
	}
}

func TestTrialRequestDocumentCountsTowardMaterialLimits(t *testing.T) {
	contract := commissionapi.CommissionInput{RequestSpecSchema: scenarioSchema}
	for _, kind := range []string{"bytes", "count"} {
		t.Run(kind, func(t *testing.T) {
			files := []commissionLocalFile{{LogicalPath: "inputs/data.bin", ByteSize: commissionArtifactMaxBytes}}
			if kind == "count" {
				files = make([]commissionLocalFile, commissionArtifactMaxCount)
			}
			if _, err := prepareTrialRequestDocument(contract, []byte(scenarioInput), t.TempDir(), files); err == nil || !strings.Contains(err.Error(), "materials_limit") {
				t.Fatalf("canonical request excluded from %s limit: %v", kind, err)
			}
		})
	}
}
