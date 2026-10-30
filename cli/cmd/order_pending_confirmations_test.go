package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/config"
)

func confirmationTestOrder(id string) map[string]any {
	return map[string]any{"order_id": id, "buyer_agent_id": "42", "version": 7, "state": "awaiting_buyer_confirmation", "buyer_confirm_due_at": 1800000000000, "buyer_input": "private input", "contract": map[string]any{"title": "untrusted title"}}
}

func runPendingConfirmationsTest(t *testing.T, ctx context.Context, language string) (string, error) {
	t.Helper()
	command := newOrderPendingConfirmationsCommand()
	command.SetContext(ctx)
	var out bytes.Buffer
	command.SetOut(&out)
	_ = command.Flags().Set("language", language)
	err := command.RunE(command, nil)
	return out.String(), err
}

func TestOrderPendingConfirmationsReadsAllPagesAndRepeatsReminder(t *testing.T) {
	for _, v2 := range []bool{false, true} {
		for _, language := range []string{"zh", "en"} {
			t.Run(fmt.Sprintf("v2=%t/%s", v2, language), func(t *testing.T) {
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, request *http.Request) {
					calls++
					if request.Method != http.MethodGet || request.URL.Path != "/api/v1/orders" || request.Header.Get("Authorization") != "Bearer test-token" {
						t.Errorf("unexpected route or identity: %s %s", request.Method, request.URL.Path)
					}
					query := request.URL.Query()
					if query.Get("role") != "buyer" || query.Get("state") != "awaiting_buyer_confirmation" || query.Get("limit") != "100" {
						t.Errorf("wrong order filter: %v", query)
					}
					id, cursor := "9223372036854775807", "next +/?="
					if query.Get("cursor") != "" {
						if query.Get("cursor") != cursor {
							t.Errorf("cursor changed: %q", query.Get("cursor"))
						}
						id, cursor = "9007199254740993", ""
					}
					materialTestResponse(out, map[string]any{"orders": []any{confirmationTestOrder(id)}, "next_cursor": cursor})
				}))
				defer server.Close()
				setupOrderPaymentTest(t, server.URL, v2)
				// The new command always returns one machine-readable object.
				formatFlag = "table"
				for cycle := 0; cycle < 2; cycle++ {
					text, err := runPendingConfirmationsTest(t, context.Background(), language)
					if err != nil {
						t.Fatal(err)
					}
					var result pendingConfirmationsResult
					if json.Unmarshal([]byte(text), &result) != nil || result.Status != "pending" || result.AgentID != "42" || len(result.Orders) != 2 || result.CheckedAt <= 0 || result.SchemaVersion != "eigenflux_pending_confirmations.v1" {
						t.Fatalf("wrong result: %s", text)
					}
					if result.Orders[0].OrderID != "9223372036854775807" || result.Orders[1].OrderID != "9007199254740993" || result.Reminder == "" || strings.Contains(text, "private input") || strings.Contains(text, "untrusted title") {
						t.Fatalf("identity precision, reminder, or projection failed: %s", text)
					}
					if language == "zh" && !strings.Contains(result.Reminder, "请及时") || language == "en" && !strings.Contains(result.Reminder, "promptly") {
						t.Fatalf("wrong language: %s", result.Reminder)
					}
					for _, order := range result.Orders {
						target, err := url.Parse(order.URL)
						if err != nil || target.Query().Get("agent_id") != "42" || target.Query().Get("order_id") != order.OrderID || target.Query().Get("role") != "buyer" {
							t.Fatalf("unsafe order target: %s", order.URL)
						}
					}
				}
				if calls != 4 {
					t.Fatalf("expected one request per page per cycle, got %d", calls)
				}
			})
		}
	}
}

func TestOrderPendingConfirmationsEmpty(t *testing.T) {
	for _, raw := range []string{`{"orders":[],"next_cursor":""}`, `{"orders":null,"next_cursor":""}`} {
		t.Run(raw, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprintf(out, `{"code":0,"data":%s}`, raw)
			}))
			defer server.Close()
			setupOrderPaymentTest(t, server.URL, true)
			text, err := runPendingConfirmationsTest(t, context.Background(), "zh")
			var result pendingConfirmationsResult
			if err != nil || json.Unmarshal([]byte(text), &result) != nil || result.Status != "empty" || result.Orders == nil || len(result.Orders) != 0 || result.Reminder != "" {
				t.Fatalf("wrong empty result: %s %v", text, err)
			}
		})
	}
}

func TestOrderPendingConfirmationsPreciselyRecognizesCommissionNotEnabled(t *testing.T) {
	for _, test := range []struct {
		name       string
		httpStatus int
		body       string
		notEnabled bool
	}{
		{"current_allowlist_denial", http.StatusForbidden, `{"code":403,"msg":"commission access is not allowed"}`, true},
		{"another_forbidden", http.StatusForbidden, `{"code":403,"msg":"order access is not allowed"}`, false},
		{"message_prefix", http.StatusForbidden, `{"code":403,"msg":"commission access is not allowed: internal error"}`, false},
		{"different_business_code", http.StatusForbidden, `{"code":470,"msg":"commission access is not allowed"}`, false},
		{"typed_other_denial", http.StatusForbidden, `{"code":403,"error_code":"ACCOUNT_SUSPENDED","msg":"commission access is not allowed"}`, false},
		{"successful_http_with_error_body", http.StatusOK, `{"code":403,"msg":"commission access is not allowed"}`, false},
		{"different_http_status", http.StatusServiceUnavailable, `{"code":403,"msg":"commission access is not allowed"}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, _ *http.Request) {
				out.WriteHeader(test.httpStatus)
				_, _ = out.Write([]byte(test.body))
			}))
			defer server.Close()
			setupOrderPaymentTest(t, server.URL, true)
			text, err := runPendingConfirmationsTest(t, context.Background(), "zh")
			if !test.notEnabled {
				if err == nil || text != "" {
					t.Fatalf("unrelated failure silently treated as unavailable: %s %v", text, err)
				}
				return
			}
			var result pendingConfirmationsResult
			if err != nil || json.Unmarshal([]byte(text), &result) != nil || result.Status != "not_enabled" || result.AgentID != "42" || result.Orders == nil || len(result.Orders) != 0 || result.Reminder != "" || result.CheckedAt <= 0 {
				t.Fatalf("wrong disabled result: %s %v", text, err)
			}
		})
	}
}

func TestOrderPendingConfirmationsAccountSwitchCannotReturnNotEnabled(t *testing.T) {
	for _, v2 := range []bool{true, false} {
		t.Run(fmt.Sprintf("v2=%t", v2), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, _ *http.Request) {
				name := activeServerName()
				if v2 {
					credentials, err := auth.LoadV2Credentials(name)
					if err != nil {
						t.Error(err)
						return
					}
					credentials.AgentID = "43"
					if err := auth.SaveV2Credentials(name, credentials); err != nil {
						t.Error(err)
						return
					}
				} else {
					if err := auth.SaveCredentials(name, &auth.Credentials{AgentID: "43", AccessToken: "other-account"}); err != nil {
						t.Error(err)
						return
					}
				}
				out.WriteHeader(http.StatusForbidden)
				_, _ = out.Write([]byte(`{"code":403,"msg":"commission access is not allowed"}`))
			}))
			defer server.Close()
			setupOrderPaymentTest(t, server.URL, v2)
			text, err := runPendingConfirmationsTest(t, context.Background(), "zh")
			if err == nil || text != "" {
				t.Fatalf("changed account emitted disabled status: %s %v", text, err)
			}
		})
	}
}

func TestOrderPendingConfirmationsNotEnabledDiscardsEarlierPages(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			materialTestResponse(out, map[string]any{"orders": []any{confirmationTestOrder("901")}, "next_cursor": "next"})
			return
		}
		out.WriteHeader(http.StatusForbidden)
		_, _ = out.Write([]byte(`{"code":403,"msg":"commission access is not allowed"}`))
	}))
	defer server.Close()
	setupOrderPaymentTest(t, server.URL, true)
	text, err := runPendingConfirmationsTest(t, context.Background(), "en")
	var result pendingConfirmationsResult
	if err != nil || json.Unmarshal([]byte(text), &result) != nil || calls != 2 || result.Status != "not_enabled" || result.Orders == nil || len(result.Orders) != 0 || result.Reminder != "" {
		t.Fatalf("revoked access leaked an earlier page: %s %v", text, err)
	}
}

func TestOrderPendingConfirmationsRejectsIncompleteOrUnsafePages(t *testing.T) {
	for _, scenario := range []string{"failed_second_page", "repeated_cursor", "page_limit", "missing_orders", "missing_cursor", "invalid_json", "wrong_buyer", "wrong_state", "invalid_id", "duplicate_order", "empty_next_page"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, _ *http.Request) {
				calls++
				if scenario == "failed_second_page" && calls == 2 {
					http.Error(out, "unavailable", http.StatusServiceUnavailable)
					return
				}
				if scenario == "invalid_json" {
					_, _ = out.Write([]byte(`{"code":0,"data":{"orders":"wrong","next_cursor":""}}`))
					return
				}
				order := confirmationTestOrder(strconv.Itoa(1000 + calls))
				data := map[string]any{"orders": []any{order}, "next_cursor": ""}
				switch scenario {
				case "failed_second_page", "repeated_cursor":
					data["next_cursor"] = "same"
				case "page_limit":
					data["next_cursor"] = strconv.Itoa(calls)
				case "missing_orders":
					delete(data, "orders")
				case "missing_cursor":
					delete(data, "next_cursor")
				case "wrong_buyer":
					order["buyer_agent_id"] = "43"
				case "wrong_state":
					order["state"] = "completed"
				case "invalid_id":
					order["order_id"] = "1e3"
				case "duplicate_order":
					order["order_id"], data["next_cursor"] = "901", "next"
				case "empty_next_page":
					data["orders"], data["next_cursor"] = []any{}, "next"
				}
				materialTestResponse(out, data)
			}))
			defer server.Close()
			setupOrderPaymentTest(t, server.URL, true)
			text, err := runPendingConfirmationsTest(t, context.Background(), "en")
			if err == nil || text != "" {
				t.Fatalf("unsafe or partial response escaped: %q %v", text, err)
			}
			if calls > pendingConfirmationMaxPages {
				t.Fatalf("pagination was not bounded: %d", calls)
			}
		})
	}
}

func TestOrderPendingConfirmationsDiscardsAccountChanges(t *testing.T) {
	for _, scenario := range []string{"agent", "principal", "endpoint", "auth_removed", "legacy_token"} {
		t.Run(scenario, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, _ *http.Request) {
				name := activeServerName()
				var err error
				switch scenario {
				case "agent", "principal":
					credentials, loadErr := auth.LoadV2Credentials(name)
					if loadErr != nil {
						t.Error(loadErr)
						return
					}
					if scenario == "agent" {
						credentials.AgentID = "43"
					} else {
						credentials.PrincipalID = "25"
					}
					err = auth.SaveV2Credentials(name, credentials)
				case "endpoint":
					cfg, loadErr := config.Load()
					if loadErr != nil {
						t.Error(loadErr)
						return
					}
					err = cfg.UpdateServerWithCommission(name, "http://127.0.0.1:2", "", "")
				case "auth_removed":
					err = auth.DeleteV2Credentials(name)
				case "legacy_token":
					err = auth.SaveCredentials(name, &auth.Credentials{AgentID: "42", AccessToken: "replacement"})
				}
				if err != nil {
					t.Error(err)
				}
				materialTestResponse(out, map[string]any{"orders": []any{confirmationTestOrder("901")}, "next_cursor": ""})
			}))
			defer server.Close()
			setupOrderPaymentTest(t, server.URL, scenario != "legacy_token")
			text, err := runPendingConfirmationsTest(t, context.Background(), "zh")
			if err == nil || text != "" {
				t.Fatalf("old account data escaped: %q %v", text, err)
			}
		})
	}
}

func TestOrderPendingConfirmationsHonorsCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) { <-request.Context().Done() }))
	defer server.Close()
	setupOrderPaymentTest(t, server.URL, true)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	text, err := runPendingConfirmationsTest(t, ctx, "en")
	if err == nil || text != "" || time.Since(start) > time.Second {
		t.Fatalf("cancelled request was not bounded or emitted data: %q %v", text, err)
	}
}

func TestOrderPendingConfirmationsRejectsInvalidLanguage(t *testing.T) {
	text, err := runPendingConfirmationsTest(t, context.Background(), "fr")
	if err == nil || text != "" {
		t.Fatalf("accepted invalid language: %q %v", text, err)
	}
}
