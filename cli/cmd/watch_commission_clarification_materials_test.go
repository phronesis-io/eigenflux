package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/commissionapi"
	"cli.eigenflux.ai/internal/dispatch"
)

const buyerClarificationOriginalText = "The report is for the finance team."

func buyerClarificationTestEntry() map[string]any {
	digest := sha256.Sum256([]byte(buyerClarificationOriginalText))
	return map[string]any{"logical_path": "inputs/request.txt", "object_id": "9007199254740993", "snapshot_id": "9007199254740995", "byte_size": len(buyerClarificationOriginalText), "sha256": hex.EncodeToString(digest[:]), "uploader_agent_id": "42"}
}

func buyerClarificationTestDetail(order commissionIntakeOrder, files []map[string]any) map[string]any {
	return map[string]any{"order_id": strconv.FormatInt(int64(order.OrderID), 10), "role": "buyer", "version": order.Version, "state": order.State, "counterparty": map[string]string{"agent_id": strconv.FormatInt(int64(order.SellerID), 10)}, "files": map[string]any{"input": files, "output": []any{}}}
}

func TestPrepareBuyerClarificationMaterialsUsesOwnedInputSnapshots(t *testing.T) {
	for _, scenario := range []string{"success", "grant_without_object_id", "wrong_buyer", "wrong_order", "wrong_role", "wrong_seller", "wrong_version", "wrong_state", "missing_manifest", "wrong_uploader", "invalid_snapshot", "invalid_hash", "invalid_path", "absolute_path", "windows_path", "duplicate_path", "too_many", "too_large", "negative_size", "wrong_object", "hash_mismatch", "byte_mismatch", "grant_credentials", "identity_at_manifest", "identity_at_grant", "identity_at_download"} {
		t.Run(scenario, func(t *testing.T) {
			order := clarificationBuyerOrder()
			order.BuyerInput = ""
			entry := buyerClarificationTestEntry()
			files := []map[string]any{entry}
			detail := buyerClarificationTestDetail(order, files)
			switch scenario {
			case "wrong_buyer":
				order.BuyerID = 43
			case "wrong_order":
				detail["order_id"] = "902"
			case "wrong_role":
				detail["role"] = "seller"
			case "wrong_seller":
				detail["counterparty"] = map[string]string{"agent_id": "43"}
			case "wrong_version":
				detail["version"] = order.Version + 1
			case "wrong_state":
				detail["state"] = "in_progress"
			case "missing_manifest":
				detail["files"] = map[string]any{}
			case "wrong_uploader":
				entry["uploader_agent_id"] = "41"
			case "invalid_snapshot":
				entry["snapshot_id"] = "0"
			case "invalid_hash":
				entry["sha256"] = "not-sha256"
			case "invalid_path":
				entry["logical_path"] = "inputs/../../credentials.json"
			case "absolute_path":
				entry["logical_path"] = "/etc/passwd"
			case "windows_path":
				entry["logical_path"] = "C:/secret.txt"
			case "duplicate_path":
				detail["files"] = map[string]any{"input": []any{entry, entry}}
			case "too_many":
				oversized := make([]any, commissionMaterialMaxFiles+1)
				detail["files"] = map[string]any{"input": oversized}
			case "too_large":
				entry["byte_size"] = commissionMaterialMaxBytes + 1
			case "negative_size":
				entry["byte_size"] = -1
			}
			var watch *accountWatch
			changeIdentity := func() {
				credential, err := auth.LoadV2Credentials(watch.server.Name)
				if err != nil {
					t.Error(err)
					return
				}
				credential.AgentID = "43"
				if err = auth.SaveV2Credentials(watch.server.Name, credential); err != nil {
					t.Error(err)
				}
			}
			grants, downloads := 0, 0
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v2/console/trade/orders/901":
					if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer test-v2" {
						t.Error("manifest request is not pinned and read-only")
					}
					if scenario == "identity_at_manifest" {
						changeIdentity()
					}
					materialTestResponse(out, detail)
				case "/api/v1/orders/901/snapshots/9007199254740995/download":
					grants++
					if r.Method != http.MethodGet || r.URL.Query().Get("path") != "inputs/request.txt" || r.Header.Get("Authorization") != "Bearer test-v2" {
						t.Error("download grant lost the original input snapshot/path or buyer identity")
					}
					grant := commissionapi.TransferGrant{ObjectID: 9007199254740993, Method: http.MethodGet, URL: server.URL + "/blob?signed=private"}
					if scenario == "grant_without_object_id" {
						grant.ObjectID = 0
					}
					if scenario == "wrong_object" {
						grant.ObjectID++
					}
					if scenario == "grant_credentials" {
						grant.Headers = map[string]string{"Authorization": "private"}
					}
					if scenario == "identity_at_grant" {
						changeIdentity()
					}
					materialTestResponse(out, map[string]any{"grant": grant})
				case "/blob":
					downloads++
					if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("X-CLI-Ver") != "" {
						t.Error("API credentials leaked to object storage")
					}
					if scenario == "identity_at_download" {
						changeIdentity()
					}
					content := buyerClarificationOriginalText
					if scenario == "hash_mismatch" {
						content = strings.Repeat("x", len(content))
					}
					if scenario == "byte_mismatch" {
						content += "extra"
					}
					_, _ = io.WriteString(out, content)
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					http.NotFound(out, r)
				}
			}))
			defer server.Close()
			watch = materialTestWatch(t, server.URL)
			directory := t.TempDir()
			got, err := watch.prepareBuyerClarificationMaterials(context.Background(), order, directory)
			if scenario == "success" || scenario == "grant_without_object_id" {
				if err != nil || len(got) != 1 || grants != 1 || downloads != 1 {
					t.Fatalf("prepare: %+v %v grants=%d downloads=%d", got, err, grants, downloads)
				}
				content, readErr := os.ReadFile(got[0].Path)
				if readErr != nil || string(content) != buyerClarificationOriginalText || filepath.Dir(got[0].Path) != directory || got[0].LogicalPath != "inputs/request.txt" {
					t.Fatalf("material not confined or not authentic: %+v %q %v", got, content, readErr)
				}
				return
			}
			if err == nil || got != nil {
				t.Fatalf("invalid buyer context admitted: %+v %v", got, err)
			}
			switch scenario {
			case "wrong_object", "grant_credentials", "identity_at_grant", "hash_mismatch", "byte_mismatch", "identity_at_download":
			default:
				if grants != 0 || downloads != 0 {
					t.Fatalf("invalid manifest reached storage: grants=%d downloads=%d", grants, downloads)
				}
			}
			if (scenario == "wrong_object" || scenario == "grant_credentials" || scenario == "identity_at_grant") && downloads != 0 {
				t.Fatal("invalid grant reached object storage")
			}
		})
	}
}

func TestBuyerClarificationAnswersFromOriginalUploadedText(t *testing.T) {
	order := clarificationBuyerOrder()
	order.BuyerInput = ""
	writes, reads := 0, 0
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/orders/901":
			materialTestResponse(out, map[string]any{"order": order})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/console/trade/orders/901":
			materialTestResponse(out, buyerClarificationTestDetail(order, []map[string]any{buyerClarificationTestEntry()}))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/orders/901/snapshots/9007199254740995/download":
			if r.URL.Query().Get("path") != "inputs/request.txt" {
				t.Error("wrong original material path")
			}
			materialTestResponse(out, map[string]any{"grant": commissionapi.TransferGrant{ObjectID: 9007199254740993, Method: http.MethodGet, URL: server.URL + "/blob?signed=private"}})
		case r.Method == http.MethodGet && r.URL.Path == "/blob":
			reads++
			_, _ = io.WriteString(out, buyerClarificationOriginalText)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/orders/901/supplement-inputs":
			writes++
			var body struct {
				ExpectedVersion int64  `json:"expected_version"`
				RequestID       string `json:"request_id"`
				Answer          string `json:"answer"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || body.ExpectedVersion != order.Version || body.RequestID != order.InputRequest.RequestID || body.Answer != "The finance team." {
				t.Error("answer did not come from the current original material")
			}
			addition, _ := json.Marshal(map[string]string{"request_id": body.RequestID, "question": order.InputRequest.Question, "answer": body.Answer})
			order.BuyerInput += commissionInputClarificationSeparator + string(addition)
			order.Version++
			order.InputRequest.Status, order.InputRequest.Answer = "answered", body.Answer
			materialTestResponse(out, map[string]any{"order": order})
		default:
			t.Errorf("unexpected API %s %s", r.Method, r.URL.Path)
			http.NotFound(out, r)
		}
	}))
	defer server.Close()
	watch := newCommissionWatch(t, server.URL, "commission_order")
	installCommissionIntakeRules(t, watch)
	job := claimBuyerClarification(t, watch, order.Version)
	var localPath string
	watch.runAgent = func(_ context.Context, request dispatch.Request) (dispatch.Result, error) {
		_, raw, ok := strings.Cut(request.Prompt, "\n\nUNTRUSTED CLARIFICATION DATA:\n")
		var payload struct {
			Context struct {
				BuyerInput string                `json:"buyer_input"`
				Files      []commissionLocalFile `json:"local_files"`
			} `json:"authorized_context"`
		}
		if !ok || json.Unmarshal([]byte(raw), &payload) != nil || payload.Context.BuyerInput != "" || len(payload.Context.Files) != 1 {
			t.Fatal("original uploaded context missing")
		}
		file := payload.Context.Files[0]
		content, err := os.ReadFile(file.Path)
		if err != nil || string(content) != buyerClarificationOriginalText || file.LogicalPath != "inputs/request.txt" || !strings.HasPrefix(file.Path, watch.binding.WorkDir+string(filepath.Separator)) {
			t.Fatalf("Agent received invalid original material: %+v %q %v", file, content, err)
		}
		if strings.Contains(request.Prompt, "signed=private") || strings.Contains(request.Prompt, "test-v2") {
			t.Fatal("credentials or signed grant leaked to Agent")
		}
		localPath = file.Path
		decision := dispatch.CommissionClarificationDecision{Version: 1, RequestID: request.ID, OrderID: "901", OrderVersion: order.Version, InputRequestID: order.InputRequest.RequestID, Outcome: "answered", Answer: "The finance team.", Summary: "Read the original buyer request file"}
		result, _ := json.Marshal(decision)
		return dispatch.Result{Text: string(result)}, nil
	}
	if err := watch.dispatchCommissionClarification(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if writes != 1 || reads != 1 || order.Version != 5 || order.InputRequest.Status != "answered" {
		t.Fatalf("original text did not close clarification: writes=%d reads=%d version=%d status=%s", writes, reads, order.Version, order.InputRequest.Status)
	}
	if localPath == "" {
		t.Fatal("no verified local material was passed")
	}
	if _, err := os.Stat(localPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("private downloaded material not cleaned up: %v", err)
	}
	for _, saved := range watch.journal.Snapshot() {
		if saved.ID == job.ID && (saved.Status != "completed" || saved.Code != "commission_clarification_answered") {
			t.Fatalf("clarification result was not confirmed: %+v", saved)
		}
	}
}

func TestBuyerClarificationDoesNotRunAfterMaterialVersionChange(t *testing.T) {
	order := clarificationBuyerOrder()
	reads := 0
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/orders/901":
			materialTestResponse(out, map[string]any{"order": order})
		case "/api/v2/console/trade/orders/901":
			materialTestResponse(out, buyerClarificationTestDetail(order, []map[string]any{}))
			order.Version++
			reads++
		default:
			t.Errorf("unexpected API after version change: %s %s", r.Method, r.URL.Path)
			http.NotFound(out, r)
		}
	}))
	defer server.Close()
	watch := newCommissionWatch(t, server.URL, "commission_order")
	installCommissionIntakeRules(t, watch)
	job := claimBuyerClarification(t, watch, order.Version)
	watch.runAgent = func(context.Context, dispatch.Request) (dispatch.Result, error) {
		t.Error("Agent invoked after the material preparation version changed")
		return dispatch.Result{}, nil
	}
	if err := watch.dispatchCommissionClarification(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if reads != 1 {
		t.Fatal("material manifest was not read")
	}
	for _, saved := range watch.journal.Snapshot() {
		if saved.ID == job.ID && (saved.Status != "completed" || saved.Code != "commission_clarification_order_changed") {
			t.Fatalf("changed order was not fenced: %+v", saved)
		}
	}
}
