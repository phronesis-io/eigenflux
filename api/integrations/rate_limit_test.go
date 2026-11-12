package integrations

import (
	"bytes"
	"context"
	"io"
	"net"
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
	l, err := NewRateLimiter(&redis.Options{Addr: cache.Addr()}, config.EnterpriseRateLimitConfig{
		Mode: mode, RequestsPerIP: ip * 2, RequestsTotal: total * 2, RequestsPerApp: application * 2, ControlReservePct: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
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
	for _, cfg := range []config.EnterpriseRateLimitConfig{
		{Mode: "invalid"},
		{Mode: "enforce", RequestsPerIP: 1, RequestsTotal: 10, RequestsPerApp: 10, ControlReservePct: 50},
		{Mode: "enforce", RequestsPerIP: 2, RequestsTotal: 10, RequestsPerApp: 10, ControlReservePct: 100},
		{Mode: "enforce", RequestsPerIP: 2, RequestsTotal: 10, RequestsPerApp: 10, ControlReservePct: 50, TrustedProxyCIDRs: []string{"0.0.0.0/0"}},
	} {
		if _, err := NewRateLimiter(&redis.Options{Addr: cache.Addr()}, cfg); err == nil {
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
	var secondLimiter *RateLimiter
	for _, h := range []*server.Hertz{first, second} {
		l := testRequestLimiter(t, cache, "enforce", 1, 20, 10)
		(&Service{Store: store, RateLimiter: l}).Register(h)
		secondLimiter = l
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
	secondLimiter.business.denied.Purge()
	if got := request(second, "GET", prefix+"/agents/me/feed").StatusCode(); got != http.StatusUnauthorized || reads.Load() != 2 {
		t.Fatalf("bucket did not reopen: status=%d SQL reads=%d", got, reads.Load())
	}
	// Model a credential that was recently seen but has since been rotated.
	secondLimiter.rememberCredential(random)
	if got := request(second, "POST", prefix+controlCheckPath).StatusCode(); got != http.StatusUnauthorized || reads.Load() != 3 {
		t.Fatalf("cached credential hint bypassed live auth: status=%d SQL reads=%d", got, reads.Load())
	}
	assertRateLimit(t, request(second, "POST", prefix+controlCheckPath))
	if secondLimiter.knownCredentials.Len() != 0 || reads.Load() != 3 {
		t.Fatal("rejected credential kept its control reservation")
	}
	for _, alias := range []string{"/sdk/controlcheck", "/sdk/control:other"} {
		if got := request(second, "POST", prefix+alias).StatusCode(); got != http.StatusNotFound || reads.Load() != 3 {
			t.Fatalf("control alias reached auth: status=%d SQL reads=%d", got, reads.Load())
		}
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
	observer := testRequestLimiter(t, cache, "observe", 1, 1, 1)
	(&Service{RateLimiter: observer}).Register(observe)
	request := func(h *server.Hertz) *protocol.Response {
		return ut.PerformRequest(h.Engine, "GET", "/api/v2/integrations/agents/me/feed", nil).Result()
	}
	for range 2 {
		if got := request(observe).StatusCode(); got != http.StatusUnauthorized {
			t.Fatalf("observe mode changed HTTP response: %d", got)
		}
	}
	if got := ut.PerformRequest(observe.Engine, "POST", integrationsPrefix+"/sdk/controlcheck", nil).Result().StatusCode(); got != http.StatusUnauthorized {
		t.Fatalf("observe mode changed alias response: %d", got)
	}
	cache.Close()
	observer.business.denied.Purge()
	if got := request(observe).StatusCode(); got != http.StatusUnauthorized {
		t.Fatalf("observe mode blocked during Redis failure: %d", got)
	}
	response := request(enforce)
	if response.StatusCode() != http.StatusServiceUnavailable || string(response.Header.Peek("Retry-After")) != "5" ||
		!strings.Contains(string(response.Body()), `"code":"INTEGRATION_RATE_LIMIT_UNAVAILABLE"`) {
		t.Fatalf("Redis failure was not closed before auth: %d %s", response.StatusCode(), response.Body())
	}
}

func TestRateLimitObserveMatchesEnforcedAppAccounting(t *testing.T) {
	cache := miniredis.RunT(t)
	for _, mode := range []string{"observe", "enforce"} {
		t.Run(mode, func(t *testing.T) {
			h := server.New()
			s := &Service{RateLimiter: testRequestLimiter(t, cache, mode, 1, 20, 2)}
			h.GET("/probe", s.limitIngress, func(ctx context.Context, c *app.RequestContext) {
				c.Next(context.WithValue(ctx, appKey{}, "synthetic-app"))
			}, s.limitApplication, func(_ context.Context, c *app.RequestContext) { c.Status(http.StatusNoContent) })
			for i := range 3 {
				want := http.StatusNoContent
				if i > 0 && mode == "enforce" {
					want = http.StatusTooManyRequests
				}
				if got := ut.PerformRequest(h.Engine, "GET", "/probe", nil).Result().StatusCode(); got != want {
					t.Fatalf("request %d: status=%d want=%d", i, got, want)
				}
			}
			if got, err := cache.Get("integration:rate:" + mode + ":business:app:synthetic-app"); got != "1" || err != nil {
				t.Fatalf("application count=%q err=%v", got, err)
			}
		})
	}
}

func TestIngressReusesDenialsAndReservesControlCapacity(t *testing.T) {
	cache := miniredis.RunT(t)
	l := testRequestLimiter(t, cache, "enforce", 1, 1, 1)
	secret, _, err := integrationidentity.NewCredential()
	if err != nil {
		t.Fatal(err)
	}
	l.rememberCredential(secret)
	s := &Service{RateLimiter: l}
	h := server.New()
	auth := func(ctx context.Context, c *app.RequestContext) {
		c.Next(context.WithValue(ctx, appKey{}, "synthetic-app"))
	}
	ok := func(_ context.Context, c *app.RequestContext) { c.Status(http.StatusNoContent) }
	h.GET("/business", s.limitIngress, auth, s.limitApplication, ok)
	h.POST(integrationsPrefix+controlCheckPath, s.limitIngress, auth, s.limitApplication, ok)
	call := func(method, path string) *protocol.Response {
		return ut.PerformRequest(h.Engine, method, path, nil,
			ut.Header{Key: "Authorization", Value: "Bearer " + secret}).Result()
	}
	if got := call("GET", "/business").StatusCode(); got != http.StatusNoContent {
		t.Fatalf("first business request: %d", got)
	}
	assertRateLimit(t, call("GET", "/business"))
	commands := cache.CommandCount()
	for range 100 {
		assertRateLimit(t, call("GET", "/business"))
	}
	if extra := cache.CommandCount() - commands; extra != 0 {
		t.Fatalf("cached denials issued %d Redis commands", extra)
	}

	// A full business lane cannot take control's budget or its local slots.
	for range cap(l.business.slots) {
		l.business.slots <- struct{}{}
	}
	cache.FastForward(requestWindow)
	l.business.denied.Purge()
	if got := call("GET", "/business").StatusCode(); got != http.StatusServiceUnavailable {
		t.Fatalf("full business lane: %d", got)
	}
	if got := call("POST", integrationsPrefix+controlCheckPath).StatusCode(); got != http.StatusNoContent {
		t.Fatalf("reserved control capacity blocked: %d", got)
	}
	assertRateLimit(t, call("POST", integrationsPrefix+controlCheckPath))
}

type dropScriptReplyConn struct {
	net.Conn
	dropped *atomic.Bool
	script  bool
}

func (c *dropScriptReplyConn) Write(p []byte) (int, error) {
	c.script = bytes.Contains(bytes.ToLower(p), []byte("\r\nevalsha\r\n"))
	return c.Conn.Write(p)
}

func (c *dropScriptReplyConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	// Receiving bytes confirms execution. Simulate transport loss before the
	// Redis client sees the reply, then allow normal reconnections.
	if n > 0 && c.script && c.dropped.CompareAndSwap(false, true) {
		_ = c.Conn.Close()
		return 0, io.EOF
	}
	return n, err
}

func TestLimiterDoesNotReplayAnExecutedScriptAfterReplyLoss(t *testing.T) {
	cache := miniredis.RunT(t)
	var dropped atomic.Bool
	dialer := &net.Dialer{}
	l, err := NewRateLimiter(&redis.Options{
		Addr: cache.Addr(),
		Dialer: func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := dialer.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			return &dropScriptReplyConn{Conn: conn, dropped: &dropped}, nil
		},
	}, config.EnterpriseRateLimitConfig{
		Mode: "enforce", RequestsPerIP: 2, RequestsTotal: 2, RequestsPerApp: 2, ControlReservePct: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	if _, err := admitRequest.Load(t.Context(), l.redis).Result(); err != nil {
		t.Fatal(err)
	}
	c := app.NewContext(0)
	if l.allow(t.Context(), c, &l.business, "ingress", requestBucket{"ip", "probe-ip", 1}) ||
		c.Response.StatusCode() != http.StatusServiceUnavailable || !dropped.Load() {
		t.Fatalf("ambiguous execution should return 503, got %d, dropped=%v", c.Response.StatusCode(), dropped.Load())
	}
	if n, err := cache.Get("probe-ip"); n != "1" || err != nil {
		t.Fatalf("expected a single debit: count=%s err=%v", n, err)
	}
}

func TestIngressBucketsAreAtomicAndRejectsDoNotSpendGlobalBudget(t *testing.T) {
	cache := miniredis.RunT(t)
	a := testRequestLimiter(t, cache, "enforce", 1, 2, 1)
	b := testRequestLimiter(t, cache, "enforce", 1, 2, 1)
	attempt := func(l *RateLimiter, ip string) bool {
		c := app.NewContext(0)
		return l.allow(t.Context(), c, &l.business, "ingress",
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
