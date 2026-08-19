package consolev2

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	redis "github.com/redis/go-redis/v9"
)

type registrationRateLimits struct {
	Window    time.Duration
	IP        int64
	Subnet    int64
	PublicKey int64
	Global    int64
}

func (limits registrationRateLimits) valid() bool {
	return limits.Window > 0 && limits.IP > 0 && limits.Subnet > 0 && limits.PublicKey > 0 && limits.Global > 0
}

type publicRegistrationChallengeRequest struct {
	PublicKey      string `json:"public_key"`
	IdempotencyKey string `json:"idempotency_key"`
}

type registrationRateDecision struct {
	Allowed      bool
	RetryAfterMS int64
}

// All quota keys share one Redis cluster hash tag, so every request must pass
// every quota before any counter is incremented. Retries count as requests too;
// database idempotency prevents them from creating a second Agent.
var publicRegistrationRateScript = redis.NewScript(`
for i = 1, #KEYS do
  local count = tonumber(redis.call('GET', KEYS[i]) or '0')
  local limit = tonumber(ARGV[i + 1])
  if count >= limit then
    local ttl = redis.call('PTTL', KEYS[i])
    if ttl < 0 then ttl = tonumber(ARGV[1]) end
    return {0, i, ttl}
  end
end

for i = 1, #KEYS do
  local count = redis.call('INCR', KEYS[i])
  if count == 1 then redis.call('PEXPIRE', KEYS[i], ARGV[1]) end
end
return {1, 0, tonumber(ARGV[1])}
`)

func registrationSubnet(ipText string) string {
	ip := net.ParseIP(strings.TrimSpace(ipText))
	if ip == nil {
		return "unknown"
	}
	if ipv4 := ip.To4(); ipv4 != nil {
		return net.IP(ipv4).Mask(net.CIDRMask(24, 32)).String() + "/24"
	}
	return ip.Mask(net.CIDRMask(64, 128)).String() + "/64"
}

func (s *Service) allowPublicRegistration(ctx context.Context, clientIP, keyFingerprint string) (registrationRateDecision, error) {
	if s.redisClient == nil || !s.registrationLimits.valid() {
		return registrationRateDecision{}, fmt.Errorf("public registration limiter is unavailable")
	}
	prefix := "console:v2:registration:{v1}:"
	hashKey := func(value string) string { return keyedHash(s.otpPepper, value) }
	keys := []string{
		prefix + "ip:" + hashKey(clientIP),
		prefix + "subnet:" + hashKey(registrationSubnet(clientIP)),
		prefix + "key:" + hashKey(keyFingerprint),
		prefix + "global",
	}
	arguments := []interface{}{
		int64(s.registrationLimits.Window / time.Millisecond),
		s.registrationLimits.IP,
		s.registrationLimits.Subnet,
		s.registrationLimits.PublicKey,
		s.registrationLimits.Global,
	}
	result, err := publicRegistrationRateScript.Run(ctx, s.redisClient, keys, arguments...).Slice()
	if err != nil {
		return registrationRateDecision{}, err
	}
	if len(result) != 3 {
		return registrationRateDecision{}, fmt.Errorf("unexpected public registration limiter response")
	}
	toInt64 := func(value interface{}) (int64, bool) {
		switch typed := value.(type) {
		case int64:
			return typed, true
		case int:
			return int64(typed), true
		default:
			return 0, false
		}
	}
	allowed, ok := toInt64(result[0])
	if !ok {
		return registrationRateDecision{}, fmt.Errorf("invalid public registration limiter decision")
	}
	retryAfter, ok := toInt64(result[2])
	if !ok {
		return registrationRateDecision{}, fmt.Errorf("invalid public registration limiter retry time")
	}
	return registrationRateDecision{Allowed: allowed == 1, RetryAfterMS: retryAfter}, nil
}

func (s *Service) issuePublicRegistrationChallenge(ctx context.Context, c *app.RequestContext) {
	var req publicRegistrationChallengeRequest
	if err := decodeBody(c, &req); err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error(), nil)
		return
	}
	publicKey, err := decodePublicKey(req.PublicKey)
	if err != nil || len(req.IdempotencyKey) < 16 || len(req.IdempotencyKey) > 128 {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "idempotency_key and a valid public_key are required", nil)
		return
	}
	keyFingerprint := fingerprint(publicKey)
	clientIP := resolveV2ClientIP(c.RemoteAddr().String(),
		string(c.Request.Header.Peek("X-Forwarded-For")),
		string(c.Request.Header.Peek("X-Real-IP")), s.trustedProxyNets)
	rateContext, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	decision, rateErr := s.allowPublicRegistration(rateContext, clientIP, keyFingerprint)
	cancel()
	if rateErr != nil {
		fail(c, http.StatusServiceUnavailable, "REGISTRATION_UNAVAILABLE", "automatic Agent registration is temporarily unavailable", nil)
		return
	}
	if !decision.Allowed {
		fail(c, http.StatusTooManyRequests, "REGISTRATION_RATE_LIMITED", "automatic Agent registration is temporarily rate limited", map[string]interface{}{
			"retry_after_ms": decision.RetryAfterMS,
		})
		return
	}

	grantRequest := issueGrantRequest{
		EntitlementID:  "public-registration:" + keyFingerprint + ":" + req.IdempotencyKey,
		IdempotencyKey: req.IdempotencyKey,
		Channel:        "public_auto",
		Policy:         "limited",
		PublicKey:      req.PublicKey,
	}
	result, grantErr := s.issueBootstrapGrantRecord(grantRequest, publicKey)
	if grantErr != nil {
		if grantErr == errConflict {
			fail(c, http.StatusConflict, "REGISTRATION_IDEMPOTENCY_CONFLICT", "registration request key was reused with different input", nil)
			return
		}
		fail(c, http.StatusInternalServerError, "REGISTRATION_FAILED", "could not prepare Agent registration", nil)
		return
	}
	reply(c, http.StatusCreated, result.response())
}
