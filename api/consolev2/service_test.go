package consolev2

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	redis "github.com/redis/go-redis/v9"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	notificationrpc "eigenflux_server/kitex_gen/eigenflux/notification"
	"eigenflux_server/pkg/config"
)

type fixedIDGenerator struct{ id int64 }

func (g *fixedIDGenerator) NextID() (int64, error) {
	g.id++
	return g.id, nil
}

func TestConsoleHandoffTTL(t *testing.T) {
	if handoffTTL != 15*time.Minute {
		t.Fatalf("handoffTTL = %s, want 15m", handoffTTL)
	}
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

func TestNormalizeDeviceName(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
		ok    bool
	}{
		{name: "trimmed", value: "  Lynn-MacBook-Pro  ", want: "Lynn-MacBook-Pro", ok: true},
		{name: "empty", value: "  ", want: "", ok: true},
		{name: "control character", value: "Lynn\nMac", want: "", ok: false},
		{name: "too long", value: strings.Repeat("电", 129), want: "", ok: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := normalizeDeviceName(test.value)
			if got != test.want || ok != test.ok {
				t.Fatalf("normalizeDeviceName(%q) = (%q, %v), want (%q, %v)", test.value, got, ok, test.want, test.ok)
			}
		})
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

func TestAttentionV1RequiresControlChannel(t *testing.T) {
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewService(gdb, &fixedIDGenerator{}, &config.Config{
		ConsoleV2BootstrapSecret: "test-secret",
		ConsoleV2OTPPepper:       "test-otp-pepper",
		ConsoleV2PublicURL:       "https://console.example.test",
		EnableAgentAttentionV1:   true,
	})
	if err == nil || !strings.Contains(err.Error(), "ENABLE_CONTROL_CHANNEL_V2") {
		t.Fatalf("Attention v1 started without its command dependency: %v", err)
	}
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

func TestConsoleV2FixedOTPMatchesControlledPGCTestAccounts(t *testing.T) {
	patterns := make([]string, 0, 8)
	for _, prefix := range []string{"kairui", "lingan", "weici", "vic"} {
		patterns = append(patterns,
			prefix+"[0-9]@pgc.eigenflux.one",
			prefix+"[1-9][0-9]@pgc.eigenflux.one",
		)
	}
	svc := &Service{testEmailPatterns: patterns, testOTP: "246810"}
	for _, prefix := range []string{"kairui", "lingan", "weici", "vic"} {
		for suffix := 0; suffix <= 99; suffix++ {
			email := fmt.Sprintf("%s%d@pgc.eigenflux.one", prefix, suffix)
			otp, skipDelivery, err := svc.generateChallengeOTP(email)
			if err != nil || !skipDelivery || otp != "246810" {
				t.Fatalf("fixed V2 OTP mismatch for %s: otp=%q skip=%v err=%v", email, otp, skipDelivery, err)
			}
		}
	}
	for _, email := range []string{
		"lingan09@pgc.eigenflux.one",
		"vic100@pgc.eigenflux.one",
		"other0@pgc.eigenflux.one",
	} {
		otp, skipDelivery, err := svc.generateChallengeOTP(email)
		if err != nil || skipDelivery || len(otp) != 6 {
			t.Fatalf("non-test V2 address entered fixed OTP path: %s otp=%q skip=%v err=%v", email, otp, skipDelivery, err)
		}
	}
}

func TestConsoleV2FixedOTPPathDoesNotRequireEmailDelivery(t *testing.T) {
	svc := &Service{}
	h := server.New(server.WithHostPorts("127.0.0.1:0"))
	h.POST("/challenge", func(_ context.Context, c *app.RequestContext) {
		svc.queueEmailChallenge(c, emailJob{
			challengeID:  "efec_test",
			skipDelivery: true,
		}, 123456)
	})
	status, payload, _ := performJSON(t, h, "POST", "/challenge", map[string]interface{}{})
	if status != http.StatusAccepted {
		t.Fatalf("fixed OTP challenge required an email queue: status=%d payload=%#v", status, payload)
	}
	data := responseData(t, payload)
	if data["challenge_id"] != "efec_test" || data["accepted"] != true {
		t.Fatalf("unexpected fixed OTP challenge response: %#v", data)
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
		identity := notificationIssuerIdentity(&notificationrpc.PendingNotification{SourceType: sourceType})
		if identity == nil || identity["verification_level"] != "official" {
			t.Fatalf("%s notification did not receive platform identity", sourceType)
		}
	}
	for _, sourceType := range []string{"friend_request", "unknown", ""} {
		if identity := notificationIssuerIdentity(&notificationrpc.PendingNotification{SourceType: sourceType}); identity != nil {
			t.Fatalf("%s notification was incorrectly marked as platform official", sourceType)
		}
	}
}

func TestConsoleSessionRuntime(t *testing.T) {
	for _, test := range []struct {
		name, version, host, wantRuntime, wantName, wantVersion string
	}{
		{name: "workbuddy", version: "5.3.14", host: "openclaw/1.2.3", wantRuntime: "workbuddy/5.3.14", wantName: "workbuddy", wantVersion: "5.3.14"},
		{host: "openclaw/1.2.3", wantRuntime: "openclaw/1.2.3", wantName: "openclaw", wantVersion: "1.2.3"},
		{host: "terminal"},
	} {
		runtime, name, version := consoleSessionRuntime(test.name, test.version, test.host)
		if runtime != test.wantRuntime || name != test.wantName || version != test.wantVersion {
			t.Fatalf("consoleSessionRuntime(%q, %q, %q) = (%q, %q, %q)",
				test.name, test.version, test.host, runtime, name, version)
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
	if err := validateDraftStep(payload, 4); err == nil {
		t.Fatal("whitespace-only intent passed validation")
	}
}

func TestOnboardingIntentValidationAllowsOptionalConditionAndAction(t *testing.T) {
	var payload draftPayload
	payload.IntentActions = append(payload.IntentActions, struct {
		WatchFor          string `json:"watch_for"`
		TriggerWhen       string `json:"trigger_when"`
		ActionInstruction string `json:"action_instruction"`
		ActionPolicy      string `json:"action_policy"`
		Priority          int16  `json:"priority"`
	}{WatchFor: "AI Agent infrastructure", ActionPolicy: "analyze_only"})
	if err := validateDraftStep(payload, 4); err != nil {
		t.Fatalf("optional intent fields blocked onboarding completion: %v", err)
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
