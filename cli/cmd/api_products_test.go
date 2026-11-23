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
