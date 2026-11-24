package cmd

import (
	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/gatewaysign"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAPIProductCallSignsExactBodyAndIdempotencyKey(t *testing.T) {
	tempHome(t)
	pub, _, _, err := auth.LoadOrCreateIdentity(activeServerName())
	if err != nil {
		t.Fatal(err)
	}
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		b, _ := io.ReadAll(r.Body)
		ts, n := r.Header.Get("EF-Timestamp"), r.Header.Get("EF-Nonce")
		sig, err := base64.RawURLEncoding.DecodeString(r.Header.Get("EF-Signature"))
		data, _ := gatewaysign.Canonical("POST", r.Host, "/v1/call/3", "", ts, n, "attempt", b)
		if err != nil || !ed25519.Verify(pub, data, sig) || r.Header.Get("Authorization") != "" || r.Header.Get("EF-Key") != auth.IdentityFingerprint(pub) || string(b) != "{}" {
			t.Error("incorrect signed request")
		}
		io.WriteString(w, "{}")
	}))
	defer srv.Close()
	cmd := newAPIProductsCmd()
	cmd.SetArgs([]string{"call", "3", "--gateway", srv.URL, "--idempotency-key", "attempt"})
	cmd.SetIn(strings.NewReader("{}"))
	cmd.SetOut(io.Discard)
	if err := cmd.ExecuteContext(context.Background()); err != nil || !called {
		t.Fatalf("call=%t err=%v", called, err)
	}
}

func TestAPIProductOutputAndRecoveryFields(t *testing.T) {
	tempHome(t)
	old := formatFlag
	t.Cleanup(func() { formatFlag = old })
	status := 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "12")
		w.WriteHeader(status)
		if status == 200 {
			io.WriteString(w, `{"price_fen":9007199254740993}`)
			return
		}
		io.WriteString(w, `{"error_code":"LIMIT","console_url":"https://example.org/wallet","auto_pay_limit_fen":0}`)
	}))
	defer srv.Close()
	for _, format := range []string{"json", "table", "agent"} {
		formatFlag = format
		cmd := newAPIProductsCmd()
		var output strings.Builder
		cmd.SetOut(&output)
		cmd.SetArgs([]string{"products", "--gateway", srv.URL})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(output.String(), "9007199254740993") || (format == "table") != strings.Contains(output.String(), "PRICE_FEN") {
			t.Fatal(output.String())
		}
	}
	for _, s := range []int{403, 429} {
		status = s
		cmd := newAPIProductsCmd()
		cmd.SetArgs([]string{"products", "--gateway", srv.URL})
		err := cmd.Execute()
		if err == nil {
			t.Fatal("missing gateway error")
		}
		for _, field := range []string{"LIMIT", "auto_pay_limit_fen=0", "https://example.org/wallet", "Retry-After", "12"} {
			if !strings.Contains(err.Error(), field) {
				t.Fatal(err)
			}
		}
	}
}
