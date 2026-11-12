package integrations

import (
	"context"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/integrationidentity"
	"github.com/alicebob/miniredis/v2"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/cloudwego/hertz/pkg/protocol"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func testRequestLimiter(t *testing.T, cache *miniredis.Miniredis, mode string, ip, total, application int) *RateLimiter {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: cache.Addr(), ContextTimeoutEnabled: true, MaxRetries: -1})
	t.Cleanup(func() { _ = client.Close() })
	l, err := NewRateLimiter(client, config.EnterpriseRateLimitConfig{
		Mode: mode, RequestsPerIP: ip, RequestsTotal: total, RequestsPerApp: application,
	})
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestRateLimitTrustsOnlyVerifiedProxyChain(t *testing.T) {
	l := RateLimiter{trustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/24")}}
	for _, tc := range []struct {
		name, peer string
		forwarded  [][]byte
		want       string
	}{
		{"direct ignores spoofing", "192.0.2.10:123", [][]byte{[]byte("198.51.100.9")}, "192.0.2.10"},
		{"trusted chain ignores client prefix", "10.0.0.2:123", [][]byte{[]byte("192.0.2.99"), []byte("198.51.100.9, 10.0.0.1")}, "198.51.100.9"},
		{"malformed chain uses peer", "10.0.0.2:123", [][]byte{[]byte("198.51.100.9, invalid")}, "10.0.0.2"},
		{"mapped address canonicalized", "[::ffff:192.0.2.10]:123", nil, "192.0.2.10"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := l.clientIP(tc.peer, tc.forwarded); got != tc.want {
				t.Fatalf("client IP = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRateLimitConfiguration(t *testing.T) {
	if l, err := NewRateLimiter(nil, config.EnterpriseRateLimitConfig{Mode: "off"}); l != nil || err != nil {
		t.Fatalf("off mode requires no limiter: %v, %v", l, err)
	}
	cache := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: cache.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	for _, cfg := range []config.EnterpriseRateLimitConfig{
		{Mode: "invalid"},
		{Mode: "enforce", RequestsPerIP: 0, RequestsTotal: 10, RequestsPerApp: 10},
		{Mode: "enforce", RequestsPerIP: 1, RequestsTotal: 10, RequestsPerApp: 10, TrustedProxyCIDRs: []string{"0.0.0.0/0"}},
	} {
		if _, err := NewRateLimiter(client, cfg); err == nil {
			t.Fatalf("accepted invalid config: %+v", cfg)
		}
	}
}

func TestEnterpriseIngressLimitCoversAllRoutesBeforeSQL(t *testing.T) {
	cache := miniredis.RunT(t)
	const prefix = "/api/v2/integrations"
	first, second := server.New(), server.New()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := db.AutoMigrate(&integrationidentity.App{}); err != nil {
		t.Fatal(err)
	}
	var reads atomic.Int64
	if err := db.Callback().Query().Before("gorm:query").Register("count_integration_reads", func(*gorm.DB) { reads.Add(1) }); err != nil {
		t.Fatal(err)
	}
	store := &integrationidentity.Store{DB: db}
	for _, h := range []*server.Hertz{first, second} {
		(&Service{Store: store, RateLimiter: testRequestLimiter(t, cache, "enforce", 1, 20, 10)}).Register(h)
	}
	second.GET("/api/v1/unrelated", func(_ context.Context, c *app.RequestContext) { c.Status(http.StatusNoContent) })
	random, _, err := integrationidentity.NewCredential()
	if err != nil {
		t.Fatal(err)
	}
	request := func(h *server.Hertz, method, path string) *protocol.Response {
		return ut.PerformRequest(h.Engine, method, path, nil,
			ut.Header{Key: "Authorization", Value: "Bearer " + random},
			ut.Header{Key: "X-Forwarded-For", Value: "198.51.100.17"}).Result()
	}
	if got := request(first, "GET", prefix+"/agents/me/feed").StatusCode(); got != http.StatusUnauthorized || reads.Load() != 1 {
		t.Fatalf("valid-format unknown credential: status=%d SQL reads=%d", got, reads.Load())
	}
	for _, endpoint := range []struct{ method, path string }{
		{"POST", "/sdk/installations"}, {"POST", "/sdk/control:check"},
		{"PUT", "/agents/me/context"}, {"GET", "/agents/me/context"},
		{"GET", "/agents/me/card"}, {"POST", "/agents/me/feed"}, {"GET", "/agents/me/feed"},
	} {
		assertRateLimit(t, request(second, endpoint.method, prefix+endpoint.path))
	}
	if got := reads.Load(); got != 1 {
		t.Fatalf("rejected traffic reached SQL: reads=%d", got)
	}
	if got := request(second, "GET", "/api/v1/unrelated").StatusCode(); got != http.StatusNoContent {
		t.Fatalf("unrelated route limited: %d", got)
	}
	cache.FastForward(requestWindow)
	if got := request(second, "GET", prefix+"/agents/me/feed").StatusCode(); got != http.StatusUnauthorized || reads.Load() != 2 {
		t.Fatalf("bucket did not reopen: status=%d SQL reads=%d", got, reads.Load())
	}
}

func TestEnterpriseAppLimitUsesAuthenticatedApplication(t *testing.T) {
	cache := miniredis.RunT(t)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := db.AutoMigrate(&integrationidentity.App{}); err != nil {
		t.Fatal(err)
	}
	newApp := func(id string) string {
		t.Helper()
		secret, hash, err := integrationidentity.NewCredential()
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&integrationidentity.App{AppID: id, CredentialHash: hash, Enabled: true}).Error; err != nil {
			t.Fatal(err)
		}
		return secret
	}
	a, b := newApp("app-a"), newApp("app-b")
	h := server.New()
	(&Service{Store: &integrationidentity.Store{DB: db}, RateLimiter: testRequestLimiter(t, cache, "enforce", 30, 40, 1)}).Register(h)
	request := func(token, path, claim string) *protocol.Response {
		return ut.PerformRequest(h.Engine, "GET", "/api/v2/integrations/agents/me/"+path, nil,
			ut.Header{Key: "Authorization", Value: "Bearer " + token},
			ut.Header{Key: "X-EigenFlux-App-ID", Value: claim}).Result()
	}
	// No installation headers: reaching the handler yields a binding error.
	if got := request(a, "feed", "app-a").StatusCode(); got != http.StatusBadRequest {
		t.Fatalf("first authenticated request: %d", got)
	}
	assertRateLimit(t, request(a, "card", "app-b"))
	if got := request(b, "context", "app-a").StatusCode(); got != http.StatusBadRequest {
		t.Fatalf("unrelated app was charged: %d", got)
	}
	assertRateLimit(t, request(b, "feed", "app-b"))
}

func TestEnterpriseRateLimitObserveAndRedisFailure(t *testing.T) {
	cache := miniredis.RunT(t)
	enforce, observe := server.New(), server.New()
	(&Service{RateLimiter: testRequestLimiter(t, cache, "enforce", 1, 1, 1)}).Register(enforce)
	(&Service{RateLimiter: testRequestLimiter(t, cache, "observe", 1, 1, 1)}).Register(observe)
	request := func(h *server.Hertz) *protocol.Response {
		return ut.PerformRequest(h.Engine, "GET", "/api/v2/integrations/agents/me/feed", nil).Result()
	}
	for range 2 {
		if got := request(observe).StatusCode(); got != http.StatusUnauthorized {
			t.Fatalf("observe mode changed HTTP response: %d", got)
		}
	}
	cache.Close()
	if got := request(observe).StatusCode(); got != http.StatusUnauthorized {
		t.Fatalf("observe mode blocked during Redis failure: %d", got)
	}
	response := request(enforce)
	if response.StatusCode() != http.StatusServiceUnavailable || string(response.Header.Peek("Retry-After")) != "5" ||
		!strings.Contains(string(response.Body()), `"code":"INTEGRATION_RATE_LIMIT_UNAVAILABLE"`) {
		t.Fatalf("Redis failure was not closed before auth: %d %s", response.StatusCode(), response.Body())
	}
}

func TestIngressBucketsAreAtomicAndRejectsDoNotSpendGlobalBudget(t *testing.T) {
	cache := miniredis.RunT(t)
	a := testRequestLimiter(t, cache, "enforce", 1, 2, 1)
	b := testRequestLimiter(t, cache, "enforce", 1, 2, 1)
	attempt := func(l *RateLimiter, ip string) bool {
		c := app.NewContext(0)
		return l.allow(t.Context(), c, "ingress",
			requestBucket{"ip", "ip:" + ip, 1}, requestBucket{"total", "total", 2})
	}
	if !attempt(a, "first") {
		t.Fatal("first request rejected")
	}
	for range 5 {
		if attempt(b, "first") {
			t.Fatal("IP cap was bypassed")
		}
	}
	var admitted atomic.Int64
	var done sync.WaitGroup
	for i := range 16 {
		done.Add(1)
		go func() {
			defer done.Done()
			if attempt([]*RateLimiter{a, b}[i%2], strconv.Itoa(i)) {
				admitted.Add(1)
			}
		}()
	}
	done.Wait()
	if n := admitted.Load(); n != 1 {
		t.Fatalf("shared ingress admitted %d additional requests, want 1", n)
	}
	if got := len(cache.Keys()); got != 3 {
		t.Fatalf("globally rejected requests created IP keys: %d keys", got)
	}
	if ttl := cache.TTL("total"); ttl != time.Minute {
		t.Fatalf("unexpected shared counter TTL %s", ttl)
	}
}

func assertRateLimit(t *testing.T, response *protocol.Response) {
	t.Helper()
	retry, err := strconv.Atoi(string(response.Header.Peek("Retry-After")))
	if response.StatusCode() != http.StatusTooManyRequests || err != nil || retry < 1 || retry > 60 ||
		!strings.Contains(string(response.Body()), `"code":"INTEGRATION_RATE_LIMITED"`) ||
		!strings.Contains(string(response.Body()), `"retry_after_seconds":`+strconv.Itoa(retry)) {
		t.Fatalf("invalid rate limit response: %d %s", response.StatusCode(), response.Body())
	}
}
