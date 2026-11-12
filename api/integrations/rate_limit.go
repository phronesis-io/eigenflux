package integrations

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/integrationidentity"
	"eigenflux_server/pkg/metrics"
	"github.com/cloudwego/hertz/pkg/app"
	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/redis/go-redis/v9"
)

const (
	requestWindow          = time.Minute
	limiterTimeout         = 500 * time.Millisecond
	businessRedisSlots     = 8
	controlRedisSlots      = 2
	ingressDenialCache     = 1024
	controlCredentialTTL   = 2 * time.Minute
	controlCredentialCache = 4096
	shadowIngressDenied    = "integration_shadow_ingress_denied"
)

// Reject without incrementing any counter if a bucket is exhausted. In
// particular, one IP cannot burn the shared ingress budget after hitting its cap.
var admitRequest = redis.NewScript(`
for i, key in ipairs(KEYS) do
  local count = tonumber(redis.call('GET', key) or '0')
  if count >= tonumber(ARGV[i+1]) then
    return {i, redis.call('PTTL', key)}
  end
end
for _, key in ipairs(KEYS) do
  if redis.call('INCR', key) == 1 then redis.call('PEXPIRE', key, ARGV[1]) end
end
return {0, 0}
`)

type RateLimiter struct {
	redis            *redis.Client
	mode             string
	trustedProxies   []netip.Prefix
	business         rateLane
	control          rateLane
	knownCredentials *lru.Cache[string, time.Time]
}

type rateLane struct {
	name           string
	ip, total, app int
	slots          chan struct{}
	denied         *lru.Cache[string, time.Time]
}

func NewRateLimiter(options *redis.Options, cfg config.EnterpriseRateLimitConfig) (*RateLimiter, error) {
	mode := strings.ToLower(strings.TrimSpace(cfg.Mode))
	switch mode {
	case "", "off":
		return nil, nil
	case "observe", "enforce":
	default:
		return nil, fmt.Errorf("ENTERPRISE_RATE_LIMIT_MODE must be off, observe or enforce")
	}
	if options == nil || cfg.RequestsPerIP < 2 || cfg.RequestsTotal < 2 || cfg.RequestsPerApp < 2 {
		return nil, fmt.Errorf("enterprise rate limits require Redis and limits of at least two requests")
	}
	if cfg.ControlReservePct < 1 || cfg.ControlReservePct > 99 {
		return nil, fmt.Errorf("ENTERPRISE_RATE_LIMIT_CONTROL_RESERVE_PERCENT must be 1 to 99")
	}
	limiter := &RateLimiter{mode: mode}
	for _, raw := range cfg.TrustedProxyCIDRs {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil || prefix.Bits() == 0 || prefix.Addr().Is4In6() {
			return nil, fmt.Errorf("invalid ENTERPRISE_RATE_LIMIT_TRUSTED_PROXY_CIDRS entry %q", raw)
		}
		limiter.trustedProxies = append(limiter.trustedProxies, prefix.Masked())
	}
	reserve := func(limit int) int {
		return min(limit-1, max(1, limit/100*cfg.ControlReservePct+limit%100*cfg.ControlReservePct/100))
	}
	ip, total, application := reserve(cfg.RequestsPerIP), reserve(cfg.RequestsTotal), reserve(cfg.RequestsPerApp)
	businessDenied, err := lru.New[string, time.Time](ingressDenialCache)
	if err != nil {
		return nil, err
	}
	controlDenied, err := lru.New[string, time.Time](ingressDenialCache)
	if err != nil {
		return nil, err
	}
	limiter.knownCredentials, err = lru.New[string, time.Time](controlCredentialCache)
	if err != nil {
		return nil, err
	}
	limiter.business = rateLane{"business", cfg.RequestsPerIP - ip, cfg.RequestsTotal - total, cfg.RequestsPerApp - application, make(chan struct{}, businessRedisSlots), businessDenied}
	limiter.control = rateLane{"control", ip, total, application, make(chan struct{}, controlRedisSlots), controlDenied}

	// Counting scripts are not idempotent when Redis commits but the response
	// is lost. Disable transport replay and keep these connections separate.
	opts := *options
	opts.MaxRetries = -1
	opts.PoolSize = businessRedisSlots + controlRedisSlots
	opts.MaxActiveConns = opts.PoolSize
	opts.MinIdleConns = 0
	opts.PoolTimeout = limiterTimeout
	opts.ContextTimeoutEnabled = true
	limiter.redis = redis.NewClient(&opts)
	return limiter, nil
}

func (l *RateLimiter) Close() error {
	if l == nil {
		return nil
	}
	return l.redis.Close()
}

func (l *RateLimiter) trusted(address netip.Addr) bool {
	for _, prefix := range l.trustedProxies {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func (l *RateLimiter) clientIP(peer string, forwarded [][]byte) string {
	addressPort, err := netip.ParseAddrPort(peer)
	if err != nil {
		return "unknown"
	}
	address := addressPort.Addr().WithZone("").Unmap()
	if address.IsUnspecified() {
		return "unknown"
	}
	if l.trusted(address) {
		// Walk back from the trusted socket peer. A client can prepend entries
		// on proxies that append X-Forwarded-For, so never trust the leftmost IP.
		for i := len(forwarded) - 1; i >= 0; i-- {
			hops := strings.Split(string(forwarded[i]), ",")
			for j := len(hops) - 1; j >= 0; j-- {
				hop, err := netip.ParseAddr(strings.TrimSpace(hops[j]))
				if err != nil || hop.Zone() != "" || hop.IsUnspecified() {
					return address.String()
				}
				hop = hop.Unmap()
				if !l.trusted(hop) {
					return hop.String()
				}
			}
		}
	}
	return address.String()
}

type requestBucket struct {
	scope string
	key   string
	limit int
}

func (l *RateLimiter) lane(c *app.RequestContext) *rateLane {
	if c.FullPath() == integrationsPrefix+controlCheckPath && string(c.Path()) == integrationsPrefix+controlCheckPath {
		return &l.control
	}
	return &l.business
}

func (l *RateLimiter) rememberCredential(secret string) {
	if hash, err := integrationidentity.CredentialHash(secret); err == nil {
		l.knownCredentials.Add(hash, time.Now().Add(controlCredentialTTL))
	}
}

func (l *RateLimiter) forgetCredential(secret string) {
	if hash, err := integrationidentity.CredentialHash(secret); err == nil {
		l.knownCredentials.Remove(hash)
	}
}

func (l *RateLimiter) ingressLane(c *app.RequestContext) *rateLane {
	if l.lane(c) == &l.control {
		if secret, ok := strings.CutPrefix(string(c.GetHeader("Authorization")), "Bearer "); ok {
			// This hint only selects a rate bucket. Authentication still
			// reads current credentials from PostgreSQL on every request.
			if hash, err := integrationidentity.CredentialHash(secret); err == nil {
				if until, ok := l.knownCredentials.Get(hash); ok && until.After(time.Now()) {
					return &l.control
				}
			}
		}
	}
	return &l.business
}

func (s *Service) limitIngress(ctx context.Context, c *app.RequestContext) {
	if l := s.RateLimiter; l != nil {
		lane := l.ingressLane(c)
		address := l.clientIP(c.RemoteAddr().String(), c.Request.Header.PeekAll("X-Forwarded-For"))
		key := fmt.Sprintf("integration:rate:%s:{ingress:%s}:", l.mode, lane.name)
		if !l.allow(ctx, c, lane, "ingress",
			requestBucket{"ip", key + fmt.Sprintf("ip:%x", sha256.Sum256([]byte(address))), lane.ip},
			requestBucket{"total", key + "total", lane.total}) {
			return
		}
	}
	c.Next(ctx)
}

func (s *Service) limitApplication(ctx context.Context, c *app.RequestContext) {
	if l := s.RateLimiter; l != nil && !c.GetBool(shadowIngressDenied) {
		lane := l.lane(c)
		owner, _ := ctx.Value(appKey{}).(string)
		key := "integration:rate:" + l.mode + ":" + lane.name + ":app:" + owner
		if !l.allow(ctx, c, lane, "app", requestBucket{"app", key, lane.app}) {
			return
		}
	}
	c.Next(ctx)
}

func (l *RateLimiter) allow(ctx context.Context, c *app.RequestContext, lane *rateLane, scope string, buckets ...requestBucket) bool {
	now := time.Now()
	if scope == "ingress" {
		for _, bucket := range buckets {
			if until, ok := lane.denied.Get(bucket.key); ok && until.After(now) {
				return l.limited(c, lane, scope, bucket.scope, until.Sub(now))
			}
		}
	}
	select {
	case lane.slots <- struct{}{}:
		defer func() { <-lane.slots }()
	default:
		metrics.IntegrationRateLimitTotal.WithLabelValues(l.mode, lane.name, scope, "busy").Inc()
		return l.reject(c, scope, http.StatusServiceUnavailable, "INTEGRATION_RATE_LIMIT_UNAVAILABLE", 1)
	}
	bound, cancel := context.WithTimeout(ctx, limiterTimeout)
	defer cancel()
	keys := make([]string, 0, len(buckets))
	args := []any{requestWindow.Milliseconds()}
	for _, bucket := range buckets {
		keys = append(keys, bucket.key)
		args = append(args, bucket.limit)
	}
	result, err := admitRequest.Run(bound, l.redis, keys, args...).Int64Slice()
	if err != nil || len(result) != 2 || result[0] < 0 || result[0] > int64(len(buckets)) || result[1] < 0 {
		metrics.IntegrationRateLimitTotal.WithLabelValues(l.mode, lane.name, scope, "unavailable").Inc()
		return l.reject(c, scope, http.StatusServiceUnavailable, "INTEGRATION_RATE_LIMIT_UNAVAILABLE", 5)
	}
	if result[0] == 0 {
		metrics.IntegrationRateLimitTotal.WithLabelValues(l.mode, lane.name, scope, "allowed").Inc()
		return true
	}
	bucket := buckets[result[0]-1]
	retry := time.Duration(result[1]) * time.Millisecond
	if scope == "ingress" && retry > 0 {
		lane.denied.Add(bucket.key, now.Add(retry))
	}
	return l.limited(c, lane, scope, bucket.scope, retry)
}

func (l *RateLimiter) limited(c *app.RequestContext, lane *rateLane, stage, bucket string, retry time.Duration) bool {
	metrics.IntegrationRateLimitTotal.WithLabelValues(l.mode, lane.name, bucket, "limited").Inc()
	seconds := max(1, int((retry+time.Second-1)/time.Second))
	return l.reject(c, stage, http.StatusTooManyRequests, "INTEGRATION_RATE_LIMITED", seconds)
}

func (l *RateLimiter) reject(c *app.RequestContext, stage string, status int, code string, seconds int) bool {
	if l.mode == "observe" {
		if stage == "ingress" {
			c.Set(shadowIngressDenied, true)
		}
		return true
	}
	writeFailure(c, &integrationidentity.Error{Status: status, Code: code}, seconds)
	c.Abort()
	return false
}
