package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/config"
)

func TestCommissionCommandsRouteAuthAndAttribution(t *testing.T) {
	gatewayCalls := 0
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gatewayCalls++
		if r.URL.Path != "/api/v1/commissions/search" || r.URL.Query().Get("query") != "Go work" {
			t.Errorf("gateway request = %s %s", r.Method, r.URL.String())
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("gateway authorization = %q", got)
		}
		writeTestEnvelope(w)
	}))
	defer gateway.Close()

	var orderKeys []string
	commission := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/orders" || r.Method != http.MethodPost {
			t.Errorf("Commission request = %s %s", r.Method, r.URL.String())
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Commission authorization = %q", got)
		}
		orderKeys = append(orderKeys, r.Header.Get("Idempotency-Key"))
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if _, sent := body["agent_id"]; sent {
			t.Error("actor agent_id must not be sent")
		}
		if body["impression_id"] != "imp-123" || body["commission_id"] != float64(77) {
			t.Errorf("order body = %#v", body)
		}
		writeTestEnvelope(w)
	}))
	defer commission.Close()

	tempHome(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	active, err := cfg.GetActive("")
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.UpdateServerWithCommission(active.Name, gateway.URL, "", commission.URL); err != nil {
		t.Fatal(err)
	}
	if err := auth.SaveCredentials(active.Name, &auth.Credentials{AgentID: "42", AccessToken: "test-token"}); err != nil {
		t.Fatal(err)
	}

	oldFormat := formatFlag
	formatFlag = "json"
	t.Cleanup(func() { formatFlag = oldFormat })
	if err := commissionSearchCmd.Flags().Set("query", "Go work"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = commissionSearchCmd.Flags().Set("query", "") })
	if err := commissionSearchCmd.RunE(commissionSearchCmd, nil); err != nil {
		t.Fatal(err)
	}
	if gatewayCalls != 1 {
		t.Fatalf("gateway calls = %d", gatewayCalls)
	}

	if err := orderCreateCmd.Flags().Set("impression-id", "imp-123"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = orderCreateCmd.Flags().Set("impression-id", "") })
	for range 2 {
		if err := orderCreateCmd.RunE(orderCreateCmd, []string{"77"}); err != nil {
			t.Fatal(err)
		}
	}
	if len(orderKeys) != 2 || orderKeys[0] == "" || orderKeys[0] != orderKeys[1] {
		t.Fatalf("idempotency keys = %#v", orderKeys)
	}
}

func TestNewCommissionClientUsesCommissionOriginWithV2Credentials(t *testing.T) {
	tempHome(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	active, err := cfg.GetActive("")
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.UpdateServerWithCommission(active.Name, "https://gateway.example.com", "", "https://commission.example.com/"); err != nil {
		t.Fatal(err)
	}
	if err := auth.SaveV2Credentials(active.Name, &auth.V2Credentials{
		AgentID: "42", PrincipalID: "24", AccessToken: "v2-token", RefreshToken: "v2-refresh",
		ExpiresAt: time.Now().Add(time.Hour).UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}

	got := newCommissionClient()
	if got.BaseURL != "https://commission.example.com/api/v1" {
		t.Fatalf("Commission BaseURL = %q", got.BaseURL)
	}
	if got.Token != "v2-token" {
		t.Fatalf("Commission token = %q", got.Token)
	}
	if got.OnUnauthorized == nil {
		t.Fatal("Commission V2 client must refresh credentials after 401")
	}
}

func writeTestEnvelope(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":{}}`))
}
