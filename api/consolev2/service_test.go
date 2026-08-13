package consolev2

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"strings"
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

func TestAddPrincipalTranscriptVerifiesAndCoversKeyAndNonce(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	req := addPrincipalRequest{
		PublicKey: base64.RawURLEncoding.EncodeToString(publicKey),
		Nonce:     "efn_device_original",
		IssuedAt:  1234,
	}
	transcript, err := addPrincipalTranscript(req)
	if err != nil {
		t.Fatal(err)
	}
	signature := ed25519.Sign(privateKey, transcript)
	if !ed25519.Verify(publicKey, transcript, signature) {
		t.Fatal("valid add-device transcript signature was rejected")
	}
	req.Nonce = "efn_device_substituted"
	mutated, err := addPrincipalTranscript(req)
	if err != nil {
		t.Fatal(err)
	}
	if ed25519.Verify(publicKey, mutated, signature) {
		t.Fatal("signature remained valid after add-device nonce substitution")
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

func TestConsoleV2WebSocketRequestBoundary(t *testing.T) {
	expected := "https://console.example.test"
	if !validConsoleWebSocketRequest(expected, "console.example.test", consoleV2WebSocketProtocol, "", expected) {
		t.Fatal("valid same-origin V2 WebSocket request was rejected")
	}
	cases := []struct {
		name, origin, host, protocol, token string
	}{
		{name: "cross origin", origin: "https://evil.example", host: "console.example.test", protocol: consoleV2WebSocketProtocol},
		{name: "wrong host", origin: expected, host: "api.example.test", protocol: consoleV2WebSocketProtocol},
		{name: "query bearer", origin: expected, host: "console.example.test", protocol: consoleV2WebSocketProtocol, token: "secret"},
		{name: "missing audience", origin: expected, host: "console.example.test", protocol: "legacy"},
		{name: "origin path", origin: expected + "/path", host: "console.example.test", protocol: consoleV2WebSocketProtocol},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if validConsoleWebSocketRequest(test.origin, test.host, test.protocol, test.token, expected) {
				t.Fatal("unsafe V2 WebSocket request was accepted")
			}
		})
	}
}

func TestNotificationIssuerIdentityFailsClosed(t *testing.T) {
	for _, sourceType := range []string{"system", "milestone", "trade"} {
		identity := notificationIssuerIdentity(sourceType)
		if identity == nil || identity["verification_level"] != "official" {
			t.Fatalf("%s notification did not receive platform identity", sourceType)
		}
	}
	for _, sourceType := range []string{"friend_request", "unknown", ""} {
		if identity := notificationIssuerIdentity(sourceType); identity != nil {
			t.Fatalf("%s notification was incorrectly marked as platform official", sourceType)
		}
	}
}

func TestCommunicationResponseBudgetAndTextFallback(t *testing.T) {
	data := map[string]interface{}{"messages": []communicationMessage{{Content: strings.Repeat("🙂", 100000)}}}
	if communicationReplyFits(data) {
		t.Fatal("oversized communication payload passed the hard response budget")
	}
	message := communicationMessage{Content: strings.Repeat("🙂", 100000)}
	boundCommunicationMessage(&message, 56000)
	if !message.ContentTruncated {
		t.Fatal("oversized message was not marked truncated")
	}
	data["messages"] = []communicationMessage{message}
	if !communicationReplyFits(data) {
		t.Fatal("single-message fallback still exceeded the hard response budget")
	}
}

func TestCommunicationContextFilterDoesNotLeakUnreferencedPeers(t *testing.T) {
	contexts := map[string]communicationAgentContext{
		"1": {ProfileStatus: "available"},
		"2": {ProfileStatus: "available"},
	}
	filtered := filterCommunicationContexts(contexts, []int64{2})
	if len(filtered) != 1 || filtered["2"].ProfileStatus != "available" {
		t.Fatalf("unexpected filtered contexts: %#v", filtered)
	}
}
