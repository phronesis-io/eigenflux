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
	"github.com/redis/go-redis/v9"
)

const requestWindow = time.Minute

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
	redis          *redis.Client
	mode           string
	ip, total, app int
	trustedProxies []netip.Prefix
}

func NewRateLimiter(client *redis.Client, cfg config.EnterpriseRateLimitConfig) (*RateLimiter, error) {
	mode := strings.ToLower(strings.TrimSpace(cfg.Mode))
	switch mode {
	case "", "off":
		return nil, nil
	case "observe", "enforce":
	default:
		return nil, fmt.Errorf("ENTERPRISE_RATE_LIMIT_MODE must be off, observe or enforce")
	}
	if client == nil || cfg.RequestsPerIP <= 0 || cfg.RequestsTotal <= 0 || cfg.RequestsPerApp <= 0 {
		return nil, fmt.Errorf("enterprise rate limits require Redis and positive request limits")
	}
	limiter := &RateLimiter{redis: client, mode: mode, ip: cfg.RequestsPerIP, total: cfg.RequestsTotal, app: cfg.RequestsPerApp}
	for _, raw := range cfg.TrustedProxyCIDRs {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil || prefix.Bits() == 0 || prefix.Addr().Is4In6() {
			return nil, fmt.Errorf("invalid ENTERPRISE_RATE_LIMIT_TRUSTED_PROXY_CIDRS entry %q", raw)
		}
		limiter.trustedProxies = append(limiter.trustedProxies, prefix.Masked())
	}
	return limiter, nil
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

func (s *Service) limitIngress(ctx context.Context, c *app.RequestContext) {
	if l := s.RateLimiter; l != nil {
		address := l.clientIP(c.RemoteAddr().String(), c.Request.Header.PeekAll("X-Forwarded-For"))
		key := fmt.Sprintf("integration:rate:%s:{ingress}:", l.mode)
		if !l.allow(ctx, c, "ingress",
			requestBucket{"ip", key + fmt.Sprintf("ip:%x", sha256.Sum256([]byte(address))), l.ip},
			requestBucket{"total", key + "total", l.total}) {
			return
		}
	}
	c.Next(ctx)
}

func (s *Service) limitApplication(ctx context.Context, c *app.RequestContext) {
	if l := s.RateLimiter; l != nil {
		owner, _ := ctx.Value(appKey{}).(string)
		key := "integration:rate:" + l.mode + ":app:" + owner
		if !l.allow(ctx, c, "app", requestBucket{"app", key, l.app}) {
			return
		}
	}
	c.Next(ctx)
}

func (l *RateLimiter) allow(ctx context.Context, c *app.RequestContext, scope string, buckets ...requestBucket) bool {
	bound, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	keys := make([]string, 0, len(buckets))
	args := []any{requestWindow.Milliseconds()}
	for _, bucket := range buckets {
		keys = append(keys, bucket.key)
		args = append(args, bucket.limit)
	}
	result, err := admitRequest.Run(bound, l.redis, keys, args...).Int64Slice()
	if err != nil || len(result) != 2 || result[0] < 0 || result[0] > int64(len(buckets)) || result[1] < 0 {
		metrics.IntegrationRateLimitTotal.WithLabelValues(l.mode, scope, "unavailable").Inc()
		return l.reject(c, http.StatusServiceUnavailable, "INTEGRATION_RATE_LIMIT_UNAVAILABLE", 5)
	}
	if result[0] == 0 {
		metrics.IntegrationRateLimitTotal.WithLabelValues(l.mode, scope, "allowed").Inc()
		return true
	}
	metrics.IntegrationRateLimitTotal.WithLabelValues(l.mode, buckets[result[0]-1].scope, "limited").Inc()
	seconds := max(1, int((result[1]+999)/1000))
	return l.reject(c, http.StatusTooManyRequests, "INTEGRATION_RATE_LIMITED", seconds)
}

func (l *RateLimiter) reject(c *app.RequestContext, status int, code string, seconds int) bool {
	if l.mode == "observe" {
		return true
	}
	writeFailure(c, &integrationidentity.Error{Status: status, Code: code}, seconds)
	c.Abort()
	return false
}
