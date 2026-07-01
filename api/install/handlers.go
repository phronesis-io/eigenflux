package install

import (
	"context"
	"encoding/json"
	"fmt"
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
	// User-facing install/join URLs (the command shown on the landing page and
	// the /r/<ref> bootstrap) use the ICP-filed domain so an ad review (小红书
	// 聚光) never sees the landing page pointing at an unfiled site. Server-only
	// endpoints (the /report callback) are unaffected; override via env.
	installBaseURL = strings.TrimRight(envStr("INSTALL_BASE_URL", "https://www.eigenflux.net"), "/")
	limiter        = newIPLimiter(20, time.Minute) // ref mint per IP
)

// Register wires the install-attribution routes onto the gateway. All endpoints
// are public (marketing landing page + agent join bootstrap, no login); ref
// minting is IP rate limited so a flood of page loads can't amplify writes.
func Register(h *server.Hertz, baseURL string) {
	publicBaseURL = strings.TrimRight(baseURL, "/")
	g := h.Group("/api/v1/install")
	g.POST("/token", mintRef)
	g.POST("/report", reportInstall)
	// Agent join entry: the /install page hands the user a command pointing here
	// (not raw GitHub), so the instruction the agent reads is one we control and
	// the fetch is the earliest post-click attribution signal.
	h.GET("/r/:ref", serveRef)
}

type mintBody struct {
	UTMSource   string `json:"utm_source"`
	UTMMedium   string `json:"utm_medium"`
	UTMCampaign string `json:"utm_campaign"`
	UTMContent  string `json:"utm_content"`
	UTMTerm     string `json:"utm_term"`
	Referrer    string `json:"referrer"`
	// Platform click identifiers from the landing URL (paid traffic).
	ClickID string `json:"click_id"` // Xiaohongshu 聚光
	Twclid  string `json:"twclid"`   // X (Twitter) Ads
}

// mintRef issues a fresh referral code, persists the UTM/referrer it was minted
// for, and returns the agent join command. The client is expected to cache the
// ref (localStorage) and not re-mint on refresh; the IP rate limit is the
// server-side backstop against write amplification.
// @router /api/v1/install/token [POST]
func mintRef(_ context.Context, c *app.RequestContext) {
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
		ClickID:     trunc(body.ClickID, 128),
		Twclid:      trunc(body.Twclid, 128),
		Status:      StatusPending,
		ClientIP:    ip,
		CreatedAt:   time.Now().UnixMilli(),
	}
	if err := CreateToken(db.DB, t); err != nil {
		reply(c, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	event("install_ref_new", t.Token, "channel", t.Channel, "paid", t.ClickID != "" || t.Twclid != "")
	reply(c, http.StatusOK, 0, "success", map[string]interface{}{
		"ref":     t.Token,
		"command": installCommand(t.Token),
	})
}

type reportBody struct {
	Ref      string                 `json:"ref"`
	Metadata map[string]interface{} `json:"metadata"`
}

// reportInstall records an install for a ref and returns its attribution. The
// first report flips the ref to "installed" (the conversion); later reports for
// the same ref return attribution without re-counting it. Public and idempotent
// — install.sh, the CLI login, and the agent onboarding doc may all report the
// same ref; only the first counts as a conversion.
// @router /api/v1/install/report [POST]
func reportInstall(_ context.Context, c *app.RequestContext) {
	raw, _ := c.Body()
	var body reportBody
	if err := json.Unmarshal(raw, &body); err != nil {
		reply(c, http.StatusBadRequest, 400, "invalid JSON body", nil)
		return
	}
	if body.Ref == "" {
		reply(c, http.StatusBadRequest, 400, "ref is required", nil)
		return
	}
	if !ValidTokenFormat(body.Ref) {
		reply(c, http.StatusBadRequest, 400, "invalid ref format, expected EF-xxxxxxxx", nil)
		return
	}
	converted, t, err := ReportInstall(db.DB, body.Ref)
	if err != nil {
		if err == ErrTokenNotFound {
			reply(c, http.StatusNotFound, 404, "ref not found", nil)
			return
		}
		reply(c, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	if converted {
		event("install_report", t.Token,
			"channel", t.Channel, "utm_source", t.UTMSource, "utm_campaign", t.UTMCampaign)
	}
	// Fallback conversion signal: fires only if the /r/ fetch didn't already
	// claim it (ClaimCallback makes the platform callback exactly-once per ref).
	fireXHSCallback(t.Token)
	reply(c, http.StatusOK, 0, "success", map[string]interface{}{
		"converted": converted,
		"attribution": map[string]interface{}{
			"ref":          t.Token,
			"channel":      t.Channel,
			"utm_source":   t.UTMSource,
			"utm_medium":   t.UTMMedium,
			"utm_campaign": t.UTMCampaign,
			"utm_content":  t.UTMContent,
			"utm_term":     t.UTMTerm,
			"referrer":     t.Referrer,
			"click_id":     t.ClickID,
			"twclid":       t.Twclid,
			"report_count": t.ReportCount,
			"created_at":   t.CreatedAt,
			"reported_at":  t.ReportedAt,
		},
	})
}

// serveRef serves the agent-facing join bootstrap for a referral code and
// records the fetch as the earliest post-click attribution signal (the proxy
// conversion fed to ad platforms within their short attribution windows).
// @router /r/:ref [GET]
func serveRef(_ context.Context, c *app.RequestContext) {
	ref := c.Param("ref")
	if !ValidTokenFormat(ref) {
		c.Data(http.StatusBadRequest, "text/markdown; charset=utf-8",
			[]byte("# Invalid referral code\n\nExpected the form EF-xxxxxxxx.\n"))
		return
	}
	t, err := MarkFetched(db.DB, ref)
	if err != nil {
		c.Data(http.StatusInternalServerError, "text/markdown; charset=utf-8", []byte("# Error\n"))
		return
	}
	if t == nil {
		// Unknown ref: still serve generic join instructions, just unattributed.
		event("install_fetch_unknown", ref)
	} else {
		event("install_fetch", ref, "channel", t.Channel)
		// Primary in-window conversion signal for the ad platform optimizer.
		fireXHSCallback(ref)
	}
	c.Data(http.StatusOK, "text/markdown; charset=utf-8", []byte(renderJoinDoc(ref)))
}

func installCommand(ref string) string {
	return fmt.Sprintf("Read %s/r/%s and follow it to join EigenFlux", installBaseURL, ref)
}

// joinDocTmpl is the agent-facing bootstrap served at /r/<ref>. {REF}/{BASE} are
// substituted per request. It carries the ref through both the install and the
// login steps so attribution is captured deterministically by the CLI.
const joinDocTmpl = "# Join EigenFlux\n\n" +
	"Your referral code: **{REF}** — keep it; it attributes this install to the campaign that referred you.\n\n" +
	"## 1. Install the EigenFlux CLI and skills\n\n" +
	"```bash\ncurl -fsSL {BASE}/install.sh | sh -s -- --ref {REF}\n```\n\n" +
	"## 2. Log in — the CLI reports your referral automatically\n\n" +
	"```bash\neigenflux auth login --email <your-email> --ref {REF}\n```\n\n" +
	"If an OTP is required, complete it (keep the `--ref`):\n\n" +
	"```bash\neigenflux auth verify --challenge-id <id> --code <code> --ref {REF}\n```\n\n" +
	"Then read the `ef-profile` skill to finish onboarding. Welcome to the broadcast network.\n"

func renderJoinDoc(ref string) string {
	s := strings.ReplaceAll(joinDocTmpl, "{REF}", ref)
	return strings.ReplaceAll(s, "{BASE}", installBaseURL)
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

// event writes one funnel log line (install_ref_new / install_fetch /
// install_report). Funnel analysis reads these from Loki.
func event(ev, ref string, kv ...interface{}) {
	args := append([]interface{}{"ev", ev, "ref", ref}, kv...)
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
