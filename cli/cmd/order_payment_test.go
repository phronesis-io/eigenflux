package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/config"
)

func setupOrderPaymentTest(t *testing.T, endpoint string, v2 bool) {
	t.Helper()
	tempHome(t)
	oldServer, oldFormat := serverFlag, formatFlag
	serverFlag, formatFlag = "", "json"
	t.Cleanup(func() { serverFlag, formatFlag = oldServer, oldFormat })
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	srv, err := cfg.GetActive("")
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.UpdateServerWithCommission(srv.Name, "http://127.0.0.1:1", "", endpoint); err != nil {
		t.Fatal(err)
	}
	if v2 {
		err = auth.SaveV2Credentials(srv.Name, &auth.V2Credentials{AgentID: "42", PrincipalID: "24", AccessToken: "test-token", RefreshToken: "refresh", ExpiresAt: time.Now().Add(time.Hour).UnixMilli()})
	} else {
		err = auth.SaveCredentials(srv.Name, &auth.Credentials{AgentID: "42", AccessToken: "test-token"})
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestOrderPaymentRoutesAndPreservesAction(t *testing.T) {
	for _, v2 := range []bool{false, true} {
		for _, channel := range []string{"page", "wap"} {
			t.Run(fmt.Sprintf("v2=%t/%s", v2, channel), func(t *testing.T) {
				const orderID = "9223372036854775807"
				paymentURL := "https://openapi.alipay.com/gateway.do?sign=" + strings.Repeat("a%2Bb", 300) + "&method=alipay.trade.page.pay"
				expires := time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339)
				var keys []string
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != http.MethodPost || r.URL.Path != "/api/v1/orders/"+orderID+"/payment" {
						t.Errorf("request = %s %s", r.Method, r.URL.Path)
					}
					if r.Header.Get("Authorization") != "Bearer test-token" {
						t.Error("missing buyer credentials")
					}
					keys = append(keys, r.Header.Get(idempotencyHeader))
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if len(body) != 2 || body["order_id"] != orderID || body["channel"] != channel {
						t.Errorf("body = %#v", body)
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"payment_action": map[string]string{"provider": "alipay", "type": "redirect", "url": paymentURL, "expires_at": expires}}})
				}))
				defer server.Close()
				setupOrderPaymentTest(t, server.URL, v2)
				cmd := newOrderPaymentCommand()
				if channel != "page" {
					_ = cmd.Flags().Set("channel", channel)
				}
				for i := 0; i < 2; i++ {
					out, err := captureHeartbeatStdout(t, func() error { return cmd.RunE(cmd, []string{orderID}) })
					if err != nil {
						t.Fatal(err)
					}
					var result struct {
						Action struct {
							URL       string `json:"url"`
							ExpiresAt string `json:"expires_at"`
						} `json:"payment_action"`
					}
					if err := json.Unmarshal([]byte(out), &result); err != nil {
						t.Fatal(err)
					}
					if result.Action.URL != paymentURL || result.Action.ExpiresAt != expires {
						t.Fatal("payment URL or deadline changed")
					}
				}
				if len(keys) != 2 || keys[0] == "" || keys[0] != keys[1] {
					t.Fatalf("unstable retry keys: %v", keys)
				}
				_ = cmd.Flags().Set("idempotency-key", "buyer-payment-retry")
				formatFlag = "table"
				out, err := captureHeartbeatStdout(t, func() error { return cmd.RunE(cmd, []string{orderID}) })
				if err != nil || !strings.Contains(out, paymentURL) || !strings.Contains(out, expires) {
					t.Fatalf("table output lost payment action: %v", err)
				}
				if keys[2] != "buyer-payment-retry" {
					t.Fatal("explicit key not preserved")
				}
			})
		}
	}
}

func TestOrderPaymentRejectsInvalidArguments(t *testing.T) {
	for _, id := range []string{"0", "-1", "invalid", "9223372036854775808"} {
		cmd := newOrderPaymentCommand()
		if err := cmd.RunE(cmd, []string{id}); err == nil {
			t.Errorf("accepted ID %q", id)
		}
	}
	for _, channel := range []string{"", "app", "qr", "PAGE"} {
		cmd := newOrderPaymentCommand()
		_ = cmd.Flags().Set("channel", channel)
		if err := cmd.RunE(cmd, []string{"42"}); err == nil {
			t.Errorf("accepted channel %q", channel)
		}
	}
	cmd := newOrderPaymentCommand()
	for _, args := range [][]string{nil, {"1", "2"}} {
		if err := cmd.Args(cmd, args); err == nil {
			t.Errorf("accepted args %v", args)
		}
	}
}

func TestOrderPaymentPropagatesFailuresWithoutLink(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusConflict, http.StatusBadGateway} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(status)
				_, _ = fmt.Fprintf(w, `{"code":%d,"msg":"order unavailable"}`, status)
			}))
			defer server.Close()
			setupOrderPaymentTest(t, server.URL, false)
			cmd := newOrderPaymentCommand()
			out, err := captureHeartbeatStdout(t, func() error { return cmd.RunE(cmd, []string{"42"}) })
			if err == nil || !strings.Contains(err.Error(), "order unavailable") || out != "" || calls != 1 {
				t.Fatalf("err=%v output=%q calls=%d", err, out, calls)
			}
		})
	}
}

func TestOrderPaymentIsDiscoverable(t *testing.T) {
	for _, name := range []string{"payment", "pay"} {
		cmd, _, err := orderCmd.Find([]string{name})
		if err != nil || cmd.Name() != "payment" {
			t.Fatalf("order %s not registered", name)
		}
	}
}
