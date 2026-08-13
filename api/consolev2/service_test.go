package consolev2

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"eigenflux_server/pkg/config"
)

type fixedIDGenerator struct{ id int64 }

func (g *fixedIDGenerator) NextID() (int64, error) {
	g.id++
	return g.id, nil
}

func TestProvisionTranscriptVerifiesAndCoversMutableFields(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	req := provisionRequest{
		BootstrapGrant: "efbg_test",
		Nonce:          "efn_test",
		PublicKey:      base64.RawURLEncoding.EncodeToString(publicKey),
		IssuedAt:       1234,
		AgentName:      "Agent One",
		Draft:          []byte(`{"network_goal":"test"}`),
	}
	transcript, err := provisionTranscript(req)
	if err != nil {
		t.Fatal(err)
	}
	signature := ed25519.Sign(privateKey, transcript)
	if !ed25519.Verify(publicKey, transcript, signature) {
		t.Fatal("valid provision transcript signature was rejected")
	}
	req.AgentName = "Agent Two"
	mutated, err := provisionTranscript(req)
	if err != nil {
		t.Fatal(err)
	}
	if ed25519.Verify(publicKey, mutated, signature) {
		t.Fatal("signature remained valid after a covered field was mutated")
	}
}

func TestRefreshTranscriptVerifiesAndCoversTokenAndNonce(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	req := refreshAgentSessionRequest{
		RefreshToken: "efv2r_original",
		Nonce:        "efn_original",
		PublicKey:    base64.RawURLEncoding.EncodeToString(publicKey),
		IssuedAt:     1234,
	}
	transcript, err := refreshTranscript(req)
	if err != nil {
		t.Fatal(err)
	}
	signature := ed25519.Sign(privateKey, transcript)
	if !ed25519.Verify(publicKey, transcript, signature) {
		t.Fatal("valid refresh transcript signature was rejected")
	}
	req.Nonce = "efn_replayed_or_substituted"
	mutated, err := refreshTranscript(req)
	if err != nil {
		t.Fatal(err)
	}
	if ed25519.Verify(publicKey, mutated, signature) {
		t.Fatal("signature remained valid after refresh nonce substitution")
	}
}

func TestFingerprintUsesCanonicalKeyBytes(t *testing.T) {
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(publicKey)
	decoded, err := decodePublicKey(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if fingerprint(decoded) != fingerprint(publicKey) {
		t.Fatal("same canonical key produced different fingerprints")
	}
	if _, err := decodePublicKey(base64.RawURLEncoding.EncodeToString(publicKey[:31])); err == nil {
		t.Fatal("short Ed25519 public key was accepted")
	}
}

func TestRegisterV2RoutesDoesNotConflictWithV1(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(gdb, &fixedIDGenerator{}, &config.Config{
		ConsoleV2BootstrapSecret: "test-secret",
		ConsoleV2OTPPepper:       "test-otp-pepper",
		ConsoleV2PublicURL:       "https://console.example.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	h := server.New(server.WithHostPorts("127.0.0.1:0"))
	h.GET("/api/v1/console/today", func(_ context.Context, _ *app.RequestContext) {})
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("V2 route registration conflicted with V1: %v", recovered)
		}
	}()
	svc.Register(h)
}
