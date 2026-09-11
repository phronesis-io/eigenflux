package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/config"
	"github.com/spf13/cobra"
)

func setCommissionInputFlags(t *testing.T, command *cobra.Command, fulfillmentSkill string) {
	t.Helper()
	values := map[string]string{
		"title":                  "Repository security review",
		"capability-description": "Review a bounded repository revision",
		"request-spec-text":      "Provide the revision and repository files",
		"delivery-spec-text":     "Write the report to outputs/report.md",
		"tags":                   "security,code-review",
		"price-fen":              "1000",
		"currency":               "CNY",
		"promised-delivery-ms":   "86400000",
		"request-spec-schema":    `{"type":"object"}`,
		"delivery-spec-schema":   `{"type":"object"}`,
		"fulfillment-skill":      fulfillmentSkill,
	}
	for name, value := range values {
		if err := command.Flags().Set(name, value); err != nil {
			t.Fatalf("set --%s: %v", name, err)
		}
		name := name
		t.Cleanup(func() { _ = command.Flags().Set(name, command.Flags().Lookup(name).DefValue) })
	}
}

func TestCommissionCreateAndUpdateSendFulfillmentSkill(t *testing.T) {
	tempHome(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	active, err := cfg.GetActive("")
	if err != nil {
		t.Fatal(err)
	}

	type request struct {
		Method           string
		Path             string
		FulfillmentSkill string
	}
	requests := make([]request, 0, 2)
	commission := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input struct {
				FulfillmentSkill string `json:"fulfillment_skill"`
			} `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		requests = append(requests, request{r.Method, r.URL.Path, body.Input.FulfillmentSkill})
		writeTestEnvelope(w)
	}))
	defer commission.Close()
	if err := cfg.UpdateServerWithCommission(active.Name, "https://gateway.example.com", "", commission.URL); err != nil {
		t.Fatal(err)
	}
	if err := auth.SaveCredentials(active.Name, &auth.Credentials{AgentID: "42", AccessToken: "test-token"}); err != nil {
		t.Fatal(err)
	}

	setCommissionInputFlags(t, commissionCreateCmd, "repository-security-review")
	if err := commissionCreateCmd.RunE(commissionCreateCmd, nil); err != nil {
		t.Fatal(err)
	}
	setCommissionInputFlags(t, commissionUpdateCmd, "repository-security-review-v2")
	if err := commissionUpdateCmd.Flags().Set("expected-version", "3"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = commissionUpdateCmd.Flags().Set("expected-version", "0") })
	if err := commissionUpdateCmd.RunE(commissionUpdateCmd, []string{"77"}); err != nil {
		t.Fatal(err)
	}

	want := []request{
		{http.MethodPost, "/api/v1/commissions", "repository-security-review"},
		{http.MethodPut, "/api/v1/commissions/77/draft", "repository-security-review-v2"},
	}
	if len(requests) != len(want) {
		t.Fatalf("requests = %#v", requests)
	}
	for i := range want {
		if requests[i] != want[i] {
			t.Errorf("request %d = %#v, want %#v", i, requests[i], want[i])
		}
	}
}

func TestCommissionInputRejectsInvalidFulfillmentSkill(t *testing.T) {
	for _, skill := range []string{"", "Repository-Review", "repository/review", "repository_review", strings.Repeat("a", 65)} {
		t.Run(skill, func(t *testing.T) {
			setCommissionInputFlags(t, commissionCreateCmd, skill)
			_, err := commissionInput(commissionCreateCmd)
			if err == nil || !strings.Contains(err.Error(), "fulfillment skill") {
				t.Fatalf("commissionInput fulfillment skill %q error = %v", skill, err)
			}
		})
	}
}

func TestCommissionCommandsRouteAuthAndAttribution(t *testing.T) {
	type discoveryRequest struct {
		Query        string
		CommissionID string
	}
	var gatewayRequests []discoveryRequest
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/commissions/search" {
			t.Errorf("gateway request = %s %s", r.Method, r.URL.String())
		}
		gatewayRequests = append(gatewayRequests, discoveryRequest{
			Query:        r.URL.Query().Get("query"),
			CommissionID: r.URL.Query().Get("commission_id"),
		})
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
	if err := commissionSearchCmd.Flags().Set("query", ""); err != nil {
		t.Fatal(err)
	}
	if err := commissionSearchCmd.Flags().Set("commission-id", "9223372036854775807"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = commissionSearchCmd.Flags().Set("commission-id", "") })
	if err := commissionSearchCmd.RunE(commissionSearchCmd, nil); err != nil {
		t.Fatal(err)
	}
	wantGatewayRequests := []discoveryRequest{
		{Query: "Go work"},
		{CommissionID: "9223372036854775807"},
	}
	if len(gatewayRequests) != len(wantGatewayRequests) {
		t.Fatalf("gateway requests = %#v", gatewayRequests)
	}
	for i := range wantGatewayRequests {
		if gatewayRequests[i] != wantGatewayRequests[i] {
			t.Errorf("gateway request %d = %#v, want %#v", i, gatewayRequests[i], wantGatewayRequests[i])
		}
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

func TestCommissionOrderableUsesCommissionOriginAndAuthenticatedRoute(t *testing.T) {
	var requestPath string
	var authorization string
	commission := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestPath = r.URL.Path
		authorization = r.Header.Get("Authorization")
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
	if err := cfg.UpdateServerWithCommission(active.Name, "https://gateway.example.com", "", commission.URL); err != nil {
		t.Fatal(err)
	}
	if err := auth.SaveCredentials(active.Name, &auth.Credentials{AgentID: "42", AccessToken: "test-token"}); err != nil {
		t.Fatal(err)
	}

	if err := commissionOrderableCmd.RunE(commissionOrderableCmd, []string{"356338934101311488"}); err != nil {
		t.Fatal(err)
	}
	if requestPath != "/api/v1/commissions/356338934101311488/orderable" {
		t.Fatalf("request path = %q", requestPath)
	}
	if authorization != "Bearer test-token" {
		t.Fatalf("authorization = %q", authorization)
	}
}

func TestCommissionOrderableRejectsInvalidCommissionIDsBeforeRequest(t *testing.T) {
	for _, id := range []string{"0", "-1", "not-an-id", "9223372036854775808"} {
		t.Run(id, func(t *testing.T) {
			if err := commissionOrderableCmd.RunE(commissionOrderableCmd, []string{id}); err == nil || !strings.Contains(err.Error(), "commission ID must be a positive integer") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestCommissionSearchRequiresExactlyOneValidMode(t *testing.T) {
	tests := []struct {
		name         string
		query        string
		commissionID string
		wantParam    string
		wantValue    string
		wantError    string
	}{
		{name: "query", query: " Go work ", wantParam: "query", wantValue: "Go work"},
		{name: "commission ID", commissionID: "9223372036854775807", wantParam: "commission_id", wantValue: "9223372036854775807"},
		{name: "missing", wantError: "exactly one of --query or --commission-id is required"},
		{name: "conflicting", query: "Go work", commissionID: "42", wantError: "exactly one of --query or --commission-id is required"},
		{name: "malformed ID", commissionID: "commission-42", wantError: "--commission-id must be a positive integer"},
		{name: "zero ID", commissionID: "0", wantError: "--commission-id must be a positive integer"},
		{name: "negative ID", commissionID: "-1", wantError: "--commission-id must be a positive integer"},
		{name: "overflowing ID", commissionID: "9223372036854775808", wantError: "--commission-id must be a positive integer"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			command := &cobra.Command{Use: "search"}
			addDiscoveryFlags(command, true)
			if err := command.Flags().Set("query", tt.query); err != nil {
				t.Fatal(err)
			}
			if err := command.Flags().Set("commission-id", tt.commissionID); err != nil {
				t.Fatal(err)
			}

			params, err := discoveryParams(command, true)
			if tt.wantError != "" {
				if err == nil || err.Error() != tt.wantError {
					t.Fatalf("discoveryParams() error = %v, want %q", err, tt.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := params[tt.wantParam]; got != tt.wantValue {
				t.Errorf("%s = %q, want %q", tt.wantParam, got, tt.wantValue)
			}
		})
	}
}

func TestCommissionSavedCommandsUseCommissionOriginAndAuthenticatedRoutes(t *testing.T) {
	type request struct {
		Method string
		Path   string
		Query  string
	}
	var requests []request
	commission := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("authorization = %q", got)
		}
		requests = append(requests, request{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery})
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
	if err := cfg.UpdateServerWithCommission(active.Name, "https://gateway.example.com", "", commission.URL); err != nil {
		t.Fatal(err)
	}
	if err := auth.SaveCredentials(active.Name, &auth.Credentials{AgentID: "42", AccessToken: "test-token"}); err != nil {
		t.Fatal(err)
	}

	for _, invocation := range []struct {
		command *cobra.Command
		args    []string
	}{
		{command: commissionSaveCmd, args: []string{"77"}},
		{command: commissionUnsaveCmd, args: []string{"77"}},
	} {
		if err := invocation.command.RunE(invocation.command, invocation.args); err != nil {
			t.Fatal(err)
		}
	}
	if err := commissionSavedCmd.Flags().Set("cursor", "123"); err != nil {
		t.Fatal(err)
	}
	if err := commissionSavedCmd.Flags().Set("limit", "10"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = commissionSavedCmd.Flags().Set("cursor", "0")
		_ = commissionSavedCmd.Flags().Set("limit", "20")
	})
	if err := commissionSavedCmd.RunE(commissionSavedCmd, nil); err != nil {
		t.Fatal(err)
	}

	want := []request{
		{Method: http.MethodPut, Path: "/api/v1/commissions/77/saved"},
		{Method: http.MethodDelete, Path: "/api/v1/commissions/77/saved"},
		{Method: http.MethodGet, Path: "/api/v1/commissions/saved", Query: "cursor=123&limit=10"},
	}
	if len(requests) != len(want) {
		t.Fatalf("requests = %#v", requests)
	}
	for i := range want {
		if requests[i] != want[i] {
			t.Errorf("request %d = %#v, want %#v", i, requests[i], want[i])
		}
	}
}

func TestCommissionSavedListValidatesPagination(t *testing.T) {
	command := &cobra.Command{Use: "saved"}
	command.Flags().Int64("cursor", 0, "")
	command.Flags().Int("limit", 20, "")
	command.RunE = commissionSavedCmd.RunE

	requireError := func(name, value, want string) {
		t.Helper()
		if err := command.Flags().Set(name, value); err != nil {
			t.Fatal(err)
		}
		if err := command.RunE(command, nil); err == nil || err.Error() != want {
			t.Fatalf("%s=%s error = %v, want %q", name, value, err, want)
		}
		_ = command.Flags().Set(name, command.Flags().Lookup(name).DefValue)
	}
	requireError("cursor", "-1", "--cursor must not be negative")
	requireError("limit", "0", "--limit must be between 1 and 100")
	requireError("limit", "101", "--limit must be between 1 and 100")
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
