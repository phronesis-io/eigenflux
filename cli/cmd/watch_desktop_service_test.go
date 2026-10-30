package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/desktopnotify"
	"cli.eigenflux.ai/internal/desktopqueue"
)

func newDesktopServiceWatch(t *testing.T, endpoint string) *accountWatch {
	t.Helper()
	w := newCommissionWatch(t, endpoint, "commission_order")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.UpdateServerWithCommission(w.server.Name, endpoint, w.server.StreamEndpoint, endpoint); err != nil {
		t.Fatal(err)
	}
	srv, err := cfg.GetActive(w.server.Name)
	if err != nil {
		t.Fatal(err)
	}
	w.server = *srv
	return w
}

func desktopServiceTestMessage(t *testing.T, w *accountWatch, role string) desktopnotify.Message {
	t.Helper()
	target, err := orderDesktopURL(w.server.Endpoint, w.identity.AgentID, "901", role)
	if err != nil {
		t.Fatal(err)
	}
	return desktopnotify.Message{ID: "service:1", Title: "Commission · " + desktopServiceNamePlaceholder, Body: "View " + desktopServiceNamePlaceholder, URL: target}
}

func TestDesktopServiceNameUsesFrozenContractForBothRoles(t *testing.T) {
	for _, role := range []string{"buyer", "seller"} {
		t.Run(role, func(t *testing.T) {
			var requests atomic.Int32
			buyer, seller := "41", "42"
			if role == "buyer" {
				buyer, seller = "42", "43"
			}
			server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/api/v2/public/") {
					materialTestResponse(out, map[string]any{"public_identity": map[string]string{"display_name": "买家小明", "display_name_en": "Buyer Ming"}})
					return
				}
				requests.Add(1)
				if r.Method != http.MethodGet || r.URL.Path != "/api/v1/orders/901" || r.URL.RawQuery != "" {
					t.Errorf("unexpected order lookup: %s %s", r.Method, r.URL)
				}
				materialTestResponse(out, map[string]any{"order": map[string]any{"order_id": "901", "buyer_agent_id": buyer, "seller_agent_id": seller,
					"contract": map[string]string{"title": "  Review\n\t" + desktopServiceNamePlaceholder + "\u202e\x00  ", "buyer_input": "private token=secret"}}})
			}))
			defer server.Close()
			w := newDesktopServiceWatch(t, server.URL)
			original := desktopServiceTestMessage(t, w, role)
			message, err := w.resolveDesktopServiceName(context.Background(), original)
			wantTitle := "【Buyer · Buyer Ming】Commission · Review " + desktopServiceNamePlaceholder
			if role == "seller" {
				wantTitle = "【Seller · Buyer Ming】Commission · Review " + desktopServiceNamePlaceholder
			}
			if err != nil || message.Title != wantTitle || message.Body != "View Review "+desktopServiceNamePlaceholder {
				t.Fatalf("frozen title expansion: %+v %v", message, err)
			}
			if message.ID != original.ID || message.URL != original.URL || requests.Load() != 1 || strings.Contains(message.Body, "secret") {
				t.Fatalf("display lookup changed routing or exposed data: %+v requests=%d", message, requests.Load())
			}
		})
	}
}

func TestDesktopServiceNameRejectsUnsafeAndCrossAccountLinksBeforeRequest(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer server.Close()
	w := newDesktopServiceWatch(t, server.URL)
	base := desktopServiceTestMessage(t, w, "seller")
	for _, target := range []string{
		"javascript:alert(1)", "https://elsewhere.example/dashboard/notifications/open?agent_id=42&order_id=901&role=seller",
		strings.Replace(base.URL, "agent_id=42", "agent_id=43", 1),
		strings.Replace(base.URL, "order_id=901", "order_id=0901", 1),
		strings.Replace(base.URL, "role=seller", "role=unknown", 1),
		strings.Replace(base.URL, "/dashboard/notifications/open", "/dashboard/notifications/other", 1),
		base.URL + "&agent_id=42", base.URL + "&unexpected=1", base.URL + "#fragment", base.URL + "&token=secret",
	} {
		message := base
		message.URL = target
		if _, err := w.resolveDesktopServiceName(context.Background(), message); !errors.Is(err, errDesktopServiceName) {
			t.Fatalf("accepted invalid target %q: %v", target, err)
		}
	}
	if requests.Load() != 0 {
		t.Fatal("invalid target performed authenticated lookup")
	}
}

func TestDesktopServiceNameRejectsInvalidOrderResponses(t *testing.T) {
	for _, tc := range []struct{ name, response string }{
		{"wrong order", `{"code":0,"data":{"order":{"order_id":"902","buyer_agent_id":"41","seller_agent_id":"42","contract":{"title":"Review"}}}}`},
		{"wrong seller", `{"code":0,"data":{"order":{"order_id":"901","buyer_agent_id":"42","seller_agent_id":"43","contract":{"title":"Review"}}}}`},
		{"missing parties", `{"code":0,"data":{"order":{"order_id":"901","seller_agent_id":"42","contract":{"title":"Review"}}}}`},
		{"missing title", `{"code":0,"data":{"order":{"order_id":"901","buyer_agent_id":"41","seller_agent_id":"42","contract":{}}}}`},
		{"invalid title", `{"code":0,"data":{"order":{"order_id":"901","buyer_agent_id":"41","seller_agent_id":"42","contract":{"title":"\n\u0000\u202e "}}}}`},
		{"upstream error", `{"code":1,"msg":"private token=secret","data":{}}`},
		{"malformed", `not json`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(out, tc.response) }))
			defer server.Close()
			w := newDesktopServiceWatch(t, server.URL)
			if _, err := w.resolveDesktopServiceName(context.Background(), desktopServiceTestMessage(t, w, "seller")); err != errDesktopServiceName {
				t.Fatalf("invalid response was accepted or error leaked: %v", err)
			}
		})
	}
}

func TestDesktopServiceNameLeavesExistingQueueCopyUnchanged(t *testing.T) {
	message := desktopnotify.Message{ID: "legacy:1", Title: "Order completed", Body: "Seller · Order 901 · Account 42", URL: "https://example.com/page"}
	// No watch configuration or credentials are needed for already rendered copy.
	got, err := (&accountWatch{}).resolveDesktopServiceName(context.Background(), message)
	if err != nil || got != message {
		t.Fatalf("existing queue copy changed: %+v %v", got, err)
	}
}

func TestDesktopServiceNameNormalizationAndLength(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{" a\n\tb\u00a0c\x00\u202e ", "a b c"}, {"\x00\u202e \n", ""}, {string([]byte{0xff}), ""},
		{strings.Repeat("服", 90), strings.Repeat("服", 79) + "…"},
	} {
		if got := desktopServiceName(tc.raw); got != tc.want || utf8.RuneCountInString(got) > 80 {
			t.Fatalf("normalized title %q, want %q", got, tc.want)
		}
	}
}

func TestDesktopServiceNameFailureRemainsQueuedWithoutShowing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		http.Error(out, "private provider failure", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	w := newDesktopServiceWatch(t, server.URL)
	w.desktop, _ = desktopqueue.Open(filepath.Join(t.TempDir(), "desktop.json"), w.scope)
	original := desktopServiceTestMessage(t, w, "seller")
	if err := w.desktop.Enqueue(original); err != nil {
		t.Fatal(err)
	}
	provider := &fakeDesktopProvider{setup: func() error { return nil }}
	w.desktopProvider = provider
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var diagnostic map[string]string
	w.emit = func(kind string, data interface{}) error {
		if fields, ok := data.(map[string]string); kind == "desktop_notification_status" && ok && fields["status"] == "retry_pending" {
			diagnostic = fields
			cancel()
		}
		return nil
	}
	if err := w.desktopLoop(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("loop did not preserve retry: %v", err)
	}
	entry, ok := w.desktop.Next(time.Now().Add(2 * time.Minute))
	if provider.calls != 0 || !ok || entry.Delivered || entry.Attempts != 1 || entry.Message != original || diagnostic["code"] != "service_name_unavailable" {
		t.Fatalf("failed lookup was shown, lost or exposed: entry=%+v calls=%d diagnostic=%v", entry, provider.calls, diagnostic)
	}
}

func TestDesktopServiceNameRechecksIdentityBeforeShowing(t *testing.T) {
	var w *accountWatch
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		credentials, err := auth.LoadV2Credentials(w.server.Name)
		if err != nil {
			t.Error(err)
			return
		}
		credentials.AgentID = "43"
		if err := auth.SaveV2Credentials(w.server.Name, credentials); err != nil {
			t.Error(err)
			return
		}
		materialTestResponse(out, map[string]any{"order": map[string]any{"order_id": "901", "buyer_agent_id": "41", "seller_agent_id": "42", "contract": map[string]string{"title": "Review"}}})
	}))
	defer server.Close()
	w = newDesktopServiceWatch(t, server.URL)
	w.desktop, _ = desktopqueue.Open(filepath.Join(t.TempDir(), "desktop.json"), w.scope)
	if err := w.desktop.Enqueue(desktopServiceTestMessage(t, w, "seller")); err != nil {
		t.Fatal(err)
	}
	provider := &fakeDesktopProvider{setup: func() error { return nil }}
	w.desktopProvider = provider
	if err := w.desktopLoop(context.Background()); !errors.Is(err, errWatchIdentity) || provider.calls != 0 {
		t.Fatalf("account changed during lookup: err=%v shows=%d", err, provider.calls)
	}
}

func TestDesktopServiceNameHonorsDeliveryContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	w := newDesktopServiceWatch(t, server.URL)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := w.resolveDesktopServiceName(ctx, desktopServiceTestMessage(t, w, "seller")); !errors.Is(err, errDesktopServiceName) {
		t.Fatalf("cancelled delivery continued: %v", err)
	}
}

func TestDesktopServiceNameValidatesExpandedNotification(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(out).Encode(map[string]any{"code": 0, "data": map[string]any{"order": map[string]any{"order_id": "901", "buyer_agent_id": "41", "seller_agent_id": "42", "contract": map[string]string{"title": strings.Repeat("x", 80)}}}})
	}))
	defer server.Close()
	w := newDesktopServiceWatch(t, server.URL)
	message := desktopServiceTestMessage(t, w, "seller")
	message.Title = strings.Repeat("x", 170) + desktopServiceNamePlaceholder
	if _, err := w.resolveDesktopServiceName(context.Background(), message); !errors.Is(err, errDesktopServiceName) {
		t.Fatalf("oversized expanded message accepted: %v", err)
	}
}

func TestDesktopRecipientIdentityFallbackAndLanguages(t *testing.T) {
	for _, language := range []string{"zh", "en"} {
		for _, name := range []string{"小明", "", "{service_name}\n\u202e小明"} {
			t.Run(language+name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/api/v2/public/agents/by-id/42/card" {
						if r.Header.Get("Authorization") != "" {
							t.Error("public lookup sent credentials")
						}
						materialTestResponse(out, map[string]any{"public_identity": map[string]string{"display_name": name, "display_name_en": name}})
						return
					}
					materialTestResponse(out, map[string]any{"order": map[string]any{"order_id": "901", "buyer_agent_id": "41", "seller_agent_id": "42", "contract": map[string]string{"title": "歌词"}}})
				}))
				defer server.Close()
				w := newDesktopServiceWatch(t, server.URL)
				for key := range desktopOrderCopy {
					message := desktopServiceTestMessage(t, w, "seller")
					message.Title, message.Body = desktopCopyText(language, "seller", key)
					got, err := w.resolveDesktopServiceName(context.Background(), message)
					if err != nil {
						t.Fatal(err)
					}
					expected := desktopServiceName(name)
					if expected == "" {
						expected = "Agent 42"
					}
					prefix := "【Seller · " + expected + "】"
					if language == "zh" {
						prefix = "【卖方 · " + expected + "】"
					}
					if !strings.HasPrefix(got.Title, prefix) {
						t.Fatalf("%s: %s", key, got.Title)
					}
					if got.URL != message.URL || got.ID != message.ID {
						t.Fatal("routing changed")
					}
				}
			})
		}
	}
}

func TestDesktopAccountPrefixUsesReceiverForBothRoles(t *testing.T) {
	for _, role := range []string{"buyer", "seller"} {
		for _, language := range []string{"zh", "en"} {
			t.Run(role+language, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
					if strings.HasPrefix(r.URL.Path, "/api/v2/public/") {
						if r.URL.Path != "/api/v2/public/agents/by-id/42/card" {
							t.Errorf("looked up counterparty: %s", r.URL.Path)
						}
						materialTestResponse(out, map[string]any{"public_identity": map[string]string{"display_name": "当前账号", "display_name_en": "Current Agent"}})
						return
					}
					buyer, seller := "42", "43"
					if role == "seller" {
						buyer, seller = "41", "42"
					}
					materialTestResponse(out, map[string]any{"order": map[string]any{"order_id": "901", "buyer_agent_id": buyer, "seller_agent_id": seller, "contract": map[string]string{"title": "报告"}}})
				}))
				defer server.Close()
				w := newDesktopServiceWatch(t, server.URL)
				m := desktopServiceTestMessage(t, w, role)
				m.Title, m.Body = desktopCopyText(language, role, "awaiting_buyer_confirmation")
				got, err := w.resolveDesktopServiceName(context.Background(), m)
				if err != nil {
					t.Fatal(err)
				}
				label, name := "买方", "当前账号"
				if role == "seller" {
					label = "卖方"
				}
				if language == "en" {
					name = "Current Agent"
					label = "Buyer"
					if role == "seller" {
						label = "Seller"
					}
				}
				if !strings.HasPrefix(got.Title, "【"+label+" · "+name+"】") || got.ID != m.ID || got.URL != m.URL {
					t.Fatalf("wrong receiver prefix or routing: %+v", got)
				}
			})
		}
	}
}
