package consolev2

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	redis "github.com/redis/go-redis/v9"
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
		IdempotencyKey: "provision-test-request",
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
		RefreshToken:      "efv2r_original",
		RotationRequestID: "refresh-test-request",
		Nonce:             "efn_original",
		PublicKey:         base64.RawURLEncoding.EncodeToString(publicKey),
		IssuedAt:          1234,
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

func TestConsoleV2RESTSameOriginBoundary(t *testing.T) {
	expected := "https://console.example.test"
	if !validConsoleSameOrigin(expected, "console.example.test", expected) {
		t.Fatal("valid same-origin V2 REST request was rejected")
	}
	for _, test := range []struct{ origin, host string }{
		{origin: "https://evil.example", host: "console.example.test"},
		{origin: expected, host: "api.example.test"},
		{origin: expected + "/path", host: "console.example.test"},
		{origin: "http://console.example.test", host: "console.example.test"},
		{origin: "", host: "console.example.test"},
	} {
		if validConsoleSameOrigin(test.origin, test.host, expected) {
			t.Fatalf("unsafe V2 REST request was accepted: %#v", test)
		}
	}
}

func TestV2ClientIPOnlyTrustsConfiguredProxy(t *testing.T) {
	_, trusted, err := net.ParseCIDR("10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	if got := resolveV2ClientIP("203.0.113.10:4321", "198.51.100.8", "", []*net.IPNet{trusted}); got != "203.0.113.10" {
		t.Fatalf("untrusted caller forged forwarded IP: %s", got)
	}
	if got := resolveV2ClientIP("10.1.2.3:4321", "198.51.100.8, 10.2.3.4", "", []*net.IPNet{trusted}); got != "198.51.100.8" {
		t.Fatalf("trusted proxy client IP = %s", got)
	}
}

func TestRegistrationSubnetUsesIPv4Slash24AndIPv6Slash64(t *testing.T) {
	for input, expected := range map[string]string{
		"203.0.113.81":         "203.0.113.0/24",
		"2001:db8:abcd:12::99": "2001:db8:abcd:12::/64",
		"not-an-ip":            "unknown",
	} {
		if actual := registrationSubnet(input); actual != expected {
			t.Fatalf("registrationSubnet(%q)=%q want %q", input, actual, expected)
		}
	}
}

func TestPublicRegistrationRequiresBootstrapSecretAndPositiveLimits(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	base := config.Config{
		EnablePublicRegistration: true,
		ConsoleV2OTPPepper:       "test-otp-pepper",
		ConsoleV2PublicURL:       "https://console.example.test",
		ConsoleV2Registration: config.RegLimit{
			WindowSec: 86400, IPLimit: 500, SubnetLimit: 500, KeyLimit: 5, GlobalLimit: 1000,
		},
	}
	if _, err := NewService(gdb, &fixedIDGenerator{}, &base); err == nil || !strings.Contains(err.Error(), "BOOTSTRAP_SECRET") {
		t.Fatalf("public registration accepted an empty bootstrap secret: %v", err)
	}
	base.ConsoleV2BootstrapSecret = "test-bootstrap-secret"
	base.ConsoleV2Registration.KeyLimit = 0
	if _, err := NewService(gdb, &fixedIDGenerator{}, &base); err == nil || !strings.Contains(err.Error(), "limits") {
		t.Fatalf("public registration accepted invalid limits: %v", err)
	}
}

func TestPublicRegistrationRateLimiterAppliesEveryDimension(t *testing.T) {
	for _, test := range []struct {
		name      string
		limits    registrationRateLimits
		firstIP   string
		firstKey  string
		secondIP  string
		secondKey string
	}{
		{
			name: "ip", limits: registrationRateLimits{Window: time.Hour, IP: 1, Subnet: 10, PublicKey: 10, Global: 10},
			firstIP: "203.0.113.10", firstKey: "key-a", secondIP: "203.0.113.10", secondKey: "key-b",
		},
		{
			name: "subnet", limits: registrationRateLimits{Window: time.Hour, IP: 10, Subnet: 1, PublicKey: 10, Global: 10},
			firstIP: "203.0.113.10", firstKey: "key-a", secondIP: "203.0.113.11", secondKey: "key-b",
		},
		{
			name: "public key", limits: registrationRateLimits{Window: time.Hour, IP: 10, Subnet: 10, PublicKey: 1, Global: 10},
			firstIP: "203.0.113.10", firstKey: "key-a", secondIP: "198.51.100.10", secondKey: "key-a",
		},
		{
			name: "global", limits: registrationRateLimits{Window: time.Hour, IP: 10, Subnet: 10, PublicKey: 10, Global: 1},
			firstIP: "203.0.113.10", firstKey: "key-a", secondIP: "198.51.100.10", secondKey: "key-b",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := miniredis.RunT(t)
			client := redis.NewClient(&redis.Options{Addr: server.Addr()})
			t.Cleanup(func() { _ = client.Close() })
			service := &Service{redisClient: client, otpPepper: "test-registration-pepper", registrationLimits: test.limits}

			first, err := service.allowPublicRegistration(context.Background(), test.firstIP, test.firstKey)
			if err != nil || !first.Allowed {
				t.Fatalf("first request decision=%#v err=%v", first, err)
			}
			second, err := service.allowPublicRegistration(context.Background(), test.secondIP, test.secondKey)
			if err != nil {
				t.Fatal(err)
			}
			if second.Allowed || second.RetryAfterMS <= 0 {
				t.Fatalf("second request was not rate limited: %#v", second)
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

func TestOnboardingStepValidationIgnoresIncompleteFutureSteps(t *testing.T) {
	var payload draftPayload
	payload.IdentityCard.AgentName = "Agent"
	if err := validateDraftStep(payload, 2); err != nil {
		t.Fatalf("step 2 was blocked by incomplete future fields: %v", err)
	}
}

func TestOnboardingAllowsReconfirmingUnlockedPreviousStepWithoutMovingCursorBack(t *testing.T) {
	if !canConfirmOnboardingStep("in_progress", 4, 2) {
		t.Fatal("an already unlocked step should remain confirmable")
	}
	if got := nextOnboardingStep(4, 2); got != 4 {
		t.Fatalf("re-confirming step 2 moved cursor to %d, want 4", got)
	}
	if canConfirmOnboardingStep("in_progress", 3, 4) {
		t.Fatal("a locked future step must not be confirmable")
	}
	if got := nextOnboardingStep(3, 3); got != 4 {
		t.Fatalf("confirming current step moved cursor to %d, want 4", got)
	}
}

func TestOnboardingIntentValidationRejectsWhitespace(t *testing.T) {
	var payload draftPayload
	payload.IntentActions = append(payload.IntentActions, struct {
		WatchFor          string `json:"watch_for"`
		TriggerWhen       string `json:"trigger_when"`
		ActionInstruction string `json:"action_instruction"`
		ActionPolicy      string `json:"action_policy"`
		Priority          int16  `json:"priority"`
	}{WatchFor: "   ", TriggerWhen: "signal", ActionInstruction: "report", ActionPolicy: "analyze_only"})
	if err := validateDraftStep(payload, 5); err == nil {
		t.Fatal("whitespace-only intent passed validation")
	}
}

func TestProcessStreamLimitIsSharedAcrossStreamKinds(t *testing.T) {
	service := &Service{processStreamTotal: maxProcessStreams - 1}
	if !service.tryAcquireProcessStream() {
		t.Fatal("last process stream slot was rejected")
	}
	if service.tryAcquireProcessStream() {
		t.Fatal("process stream limit was exceeded")
	}
	service.releaseProcessStream()
	if !service.tryAcquireProcessStream() {
		t.Fatal("released process stream slot was not reusable")
	}
}
