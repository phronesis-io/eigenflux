package install

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"

	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"
)

// Package-level deps, set once by Register (same pattern as api/agti).
var (
	publicBaseURL string
	limiter       = newIPLimiter(20, time.Minute) // token mint per IP
)

// Register wires the install-attribution routes onto the gateway. Both
// endpoints are public (marketing landing page, no login); token minting is
// IP rate limited so a flood of page loads can't amplify writes.
func Register(h *server.Hertz, baseURL string) {
	publicBaseURL = strings.TrimRight(baseURL, "/")
	g := h.Group("/api/v1/install")
	g.POST("/token", mintToken)
	g.POST("/report", reportInstall)
}

type mintBody struct {
	UTMSource   string `json:"utm_source"`
	UTMMedium   string `json:"utm_medium"`
	UTMCampaign string `json:"utm_campaign"`
	UTMContent  string `json:"utm_content"`
	UTMTerm     string `json:"utm_term"`
	Referrer    string `json:"referrer"`
}

// mintToken issues a fresh attribution token, persists the UTM/referrer it was
// minted for, and returns the agent install command. The client is expected to
// cache the token (localStorage) and not re-mint on refresh; the IP rate limit
// is the server-side backstop against write amplification.
// @router /api/v1/install/token [POST]
func mintToken(_ context.Context, c *app.RequestContext) {
	ip := clientIP(c)
	if !limiter.Allow(ip) {
		reply(c, http.StatusTooManyRequests, 429, "rate limited, try again later", nil)
		return
	}
	var body mintBody
	if raw, _ := c.Body(); len(raw) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			reply(c, http.StatusBadRequest, 400, "invalid JSON body", nil)
			return
		}
	}
	t := &Token{
		Token:       NewToken(),
		UTMSource:   trunc(body.UTMSource, 255),
		UTMMedium:   trunc(body.UTMMedium, 255),
		UTMCampaign: trunc(body.UTMCampaign, 255),
		UTMContent:  trunc(body.UTMContent, 255),
		UTMTerm:     trunc(body.UTMTerm, 255),
		Channel:     normalizeChannel(body.UTMSource),
		Referrer:    trunc(body.Referrer, 2048),
		Status:      StatusPending,
		ClientIP:    ip,
		CreatedAt:   time.Now().UnixMilli(),
	}
	if err := CreateToken(db.DB, t); err != nil {
		reply(c, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	event("install_token_new", t.Token, "channel", t.Channel)
	reply(c, http.StatusOK, 0, "success", map[string]interface{}{
		"token":   t.Token,
		"command": installCommand(t.Token),
	})
}

type reportBody struct {
	Token    string                 `json:"token"`
	Metadata map[string]interface{} `json:"metadata"`
}

// reportInstall records an install for a token and returns its attribution.
// The first report flips the token to "installed" (the conversion); later
// reports for the same token return attribution without re-counting it.
// @router /api/v1/install/report [POST]
func reportInstall(_ context.Context, c *app.RequestContext) {
	raw, _ := c.Body()
	var body reportBody
	if err := json.Unmarshal(raw, &body); err != nil {
		reply(c, http.StatusBadRequest, 400, "invalid JSON body", nil)
		return
	}
	if body.Token == "" {
		reply(c, http.StatusBadRequest, 400, "token is required", nil)
		return
	}
	if !ValidTokenFormat(body.Token) {
		reply(c, http.StatusBadRequest, 400, "invalid token format, expected EF-xxxxxxxx", nil)
		return
	}
	converted, t, err := ReportInstall(db.DB, body.Token)
	if err != nil {
		if err == ErrTokenNotFound {
			reply(c, http.StatusNotFound, 404, "token not found", nil)
			return
		}
		reply(c, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	if converted {
		event("install_report", t.Token,
			"channel", t.Channel, "utm_source", t.UTMSource, "utm_campaign", t.UTMCampaign)
	}
	reply(c, http.StatusOK, 0, "success", map[string]interface{}{
		"converted": converted,
		"attribution": map[string]interface{}{
			"token":        t.Token,
			"channel":      t.Channel,
			"utm_source":   t.UTMSource,
			"utm_medium":   t.UTMMedium,
			"utm_campaign": t.UTMCampaign,
			"utm_content":  t.UTMContent,
			"utm_term":     t.UTMTerm,
			"referrer":     t.Referrer,
			"report_count": t.ReportCount,
			"created_at":   t.CreatedAt,
			"reported_at":  t.ReportedAt,
		},
	})
}

func installCommand(token string) string {
	return "Read https://github.com/phronesis-io/eigenflux and help me join EigenFlux --invite " + token
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// --- house helpers (package-private in api/agti, re-declared here) ---

func reply(c *app.RequestContext, status int, code int, msg string, data interface{}) {
	body := map[string]interface{}{"code": code, "msg": msg}
	if data != nil {
		body["data"] = data
	}
	c.JSON(status, body)
}

func clientIP(c *app.RequestContext) string {
	for _, key := range []string{"X-Forwarded-For", "X-Real-IP"} {
		if v := string(c.GetHeader(key)); v != "" {
			if i := strings.IndexByte(v, ','); i > 0 {
				v = v[:i]
			}
			return strings.TrimSpace(v)
		}
	}
	return c.ClientIP()
}

// event writes one funnel log line (install_token_new / install_report).
// Funnel analysis reads these from Loki.
func event(ev, token string, kv ...interface{}) {
	args := append([]interface{}{"ev", ev, "token", token}, kv...)
	logger.Default().Info("install", args...)
}

// --- minimal per-IP rate limiter (fixed window), mirrored from api/agti ---

type ipLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	hits   map[string]*windowCount
}

type windowCount struct {
	start time.Time
	n     int
}

func newIPLimiter(limit int, window time.Duration) *ipLimiter {
	return &ipLimiter{limit: limit, window: window, hits: make(map[string]*windowCount)}
}

func (l *ipLimiter) Allow(ip string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	// Opportunistic cleanup so the map can't grow unbounded.
	if len(l.hits) > 4096 {
		for k, w := range l.hits {
			if now.Sub(w.start) > l.window {
				delete(l.hits, k)
			}
		}
	}
	w := l.hits[ip]
	if w == nil || now.Sub(w.start) > l.window {
		l.hits[ip] = &windowCount{start: now, n: 1}
		return true
	}
	w.n++
	return w.n <= l.limit
}
