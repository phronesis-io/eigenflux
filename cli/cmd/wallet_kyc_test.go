package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/config"
)

func TestWalletKYCCurrentBindingCommands(t *testing.T) {
	tempHome(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	active, err := cfg.GetActive("")
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing actor authentication")
		}
		if r.Method == "POST" {
			if r.Header.Get("Idempotency-Key") == "" || (!strings.HasSuffix(r.URL.Path, "/authorization") && r.Header.Get("Idempotency-Key") != "attempt-key") {
				t.Error("retry key changed")
			}
			var input map[string]string
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			if _, exists := input["logon_id"]; exists {
				t.Error("must use bound account")
			}
			if _, exists := input["agent_id"]; exists {
				t.Error("actor override")
			}
			if strings.HasSuffix(r.URL.Path, "/authorization") {
				if len(input) != 1 || input["verification_id"] != "359250691924951040" {
					t.Error("authorization must use the current verification without PII")
				}
			} else if strings.HasSuffix(r.URL.Path, "/complete") {
				if input["verification_id"] != "359250691924951040" || input["authorization"] != "private-code" {
					t.Error("invalid completion payload")
				}
			} else if input["user_name"] != "测试姓名" || input["cert_no"] != "110101199001010011" {
				t.Error("invalid initiation payload")
			} else if r.Header.Get("X-Wallet-KYC-Browser") != "1" {
				t.Error("start must require configured browser handoff")
			}
		}
		_, _ = fmt.Fprintf(w, `{"code":0,"data":{"verification":{"verification_id":"359250691924951040","binding_id":"359250691924951041","state":"pending","verify_id":"provider-id","expires_at":123,"authorization_expires_at":100,"authorization_url":"https://commission.example/api/v1/public/wallet/kyc/launch?ticket=%s"},"user_name":"private-extra"}}`, strings.Repeat("a", 64))
	}))
	defer server.Close()
	if err := cfg.UpdateServerWithCommission(active.Name, "https://gateway.example.com", "", server.URL); err != nil {
		t.Fatal(err)
	}
	if err := auth.SaveCredentials(active.Name, &auth.Credentials{AgentID: "42", AccessToken: "test-token"}); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		command, input string
		args           []string
	}{
		{"start", `{"user_name":"测试姓名","cert_no":"110101199001010011"}`, nil},
		{"complete", `{"authorization":"private-code"}`, []string{"359250691924951040"}},
		{"get", "", nil},
		{"authorize", "", []string{"359250691924951040"}},
	} {
		command, _, err := newWalletKYCCmd().Find([]string{test.command})
		if err != nil {
			t.Fatal(err)
		}
		if test.command == "start" || test.command == "complete" {
			_ = command.Flags().Set("stdin", "true")
			_ = command.Flags().Set("idempotency-key", "attempt-key")
			command.SetIn(strings.NewReader(test.input))
		}
		out, err := captureHeartbeatStdout(t, func() error { return command.RunE(command, test.args) })
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{"测试姓名", "110101199001010011", "private-code", "private-extra"} {
			if strings.Contains(out, secret) {
				t.Error("private input leaked")
			}
		}
		if test.command != "start" && test.command != "authorize" && strings.Contains(out, "provider-id") {
			t.Error("status must not expose provider ID")
		}
		if test.command == "start" && !strings.Contains(out, "provider-id") {
			t.Error("initiation must expose provider ID for fresh authorization")
		}
		if (test.command == "start" || test.command == "authorize") != strings.Contains(out, "https://commission.example/") {
			t.Error("authorization link exposure does not match command")
		}
	}
	if len(paths) != 4 || paths[0] != "POST /api/v1/wallet/kyc" || paths[1] != "POST /api/v1/wallet/kyc/complete" || paths[2] != "GET /api/v1/wallet/kyc" || paths[3] != "POST /api/v1/wallet/kyc/authorization" {
		t.Fatalf("unexpected requests: %v", paths)
	}
}

func TestWalletKYCAuthorizationURLRejectsUnsafeDestinations(t *testing.T) {
	valid := "https://commission.example/api/v1/public/wallet/kyc/launch?ticket=" + strings.Repeat("a", 64)
	if !validKYCAuthorizationURL(valid) {
		t.Fatal("valid authorization URL rejected")
	}
	for _, value := range []string{"javascript:alert(1)", strings.Replace(valid, "https:", "http:", 1), strings.Replace(valid, "commission.example", "user@commission.example", 1), valid + "&redirect=https://evil.example", valid + "#fragment", strings.Replace(valid, "launch?", "callback?", 1), valid + "&ticket=other"} {
		if validKYCAuthorizationURL(value) {
			t.Fatal("unsafe authorization URL accepted")
		}
	}
}

func TestWalletKYCRejectsPrivateArgumentOverridesAndMalformedInput(t *testing.T) {
	for _, input := range []string{`null`, `{}`, `{"user_name":"x","cert_no":"bad"}`, `{"user_name":"x","cert_no":"110101199001010011","logon_id":"other"}`, `{"user_name":"private-broken`, `{}` + `{}`, strings.Repeat("x", 4097)} {
		command, _, _ := newWalletKYCCmd().Find([]string{"start"})
		_ = command.Flags().Set("stdin", "true")
		_ = command.Flags().Set("idempotency-key", "key")
		command.SetIn(strings.NewReader(input))
		if err := command.RunE(command, nil); err == nil || strings.Contains(err.Error(), "private-broken") {
			t.Fatalf("unsafe validation error: %v", err)
		}
	}
	command, _, _ := newWalletKYCCmd().Find([]string{"start"})
	if err := command.RunE(command, nil); err == nil {
		t.Fatal("stdin and explicit key must be required")
	}
}

func TestWalletKYCProtectsErrorDetailsAndRejectsMalformedSuccess(t *testing.T) {
	upstream := &client.APIError{StatusCode: 429, Code: 429, ErrorCode: "WALLET_RATE_LIMITED", Msg: "private identity", Details: json.RawMessage(`{"cert_no":"private identity"}`), RetryAfterSeconds: 30}
	err := printWalletKYC(nil, upstream, false)
	var safe *client.APIError
	if !errors.As(err, &safe) || safe.ErrorCode != upstream.ErrorCode || safe.RetryAfterSeconds != 30 || len(safe.Details) != 0 || strings.Contains(safe.Error(), "private identity") {
		t.Fatalf("unsafe upstream error: %v", err)
	}
	for _, body := range []string{`{}`, `{"verification":null}`, `{"verification":{"binding_id":"1","verification_id":"2","state":"pending"}}`, `{"verification":{"binding_id":"private identity","verification_id":"2","state":"verified"}}`, `{"verification":{"binding_id":"1","verification_id":"2","state":"private identity"}}`} {
		if err := printWalletKYC(&client.APIResponse{Data: json.RawMessage(body)}, nil, true); err == nil || strings.Contains(err.Error(), "private identity") {
			t.Fatalf("invalid response accepted: %v", err)
		}
	}
}

func TestWalletKYCUnassessedAndTerminalStates(t *testing.T) {
	for _, state := range []string{"not_assessed", "preparing", "verified", "rejected", "failed", "expired"} {
		id := "2"
		if state == "not_assessed" {
			id = "0"
		}
		data, err := json.Marshal(map[string]any{"verification": map[string]any{
			"verification_id": id, "binding_id": "1", "state": state, "verify_id": "hidden-provider-id",
		}})
		if err != nil {
			t.Fatal(err)
		}
		out, err := captureHeartbeatStdout(t, func() error {
			return printWalletKYC(&client.APIResponse{Data: data}, nil, true)
		})
		if err != nil || !strings.Contains(out, state) || strings.Contains(out, "hidden-provider-id") {
			t.Fatalf("invalid state output: %s, %v", out, err)
		}
	}
}
