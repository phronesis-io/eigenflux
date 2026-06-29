package agti

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"

	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"
)

// Package-level deps, set once by Register (same pattern as api/clients).
var (
	bank          *Bank
	publicBaseURL string
	dashKey       string                          // funnel dashboard access key (env AGTI_DASH_KEY)
	limiter       = newIPLimiter(10, time.Minute) // session creation per IP
	refRe         = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

const (
	sessionTTL = 7 * 24 * time.Hour // unfinished sessions are deleted after this
	refWindow  = 30 * time.Minute   // IP fallback window linking skills_view → quiz_start
)

// normalizeRef validates a KOL ref code; returns "" for missing/invalid input
// so untracked traffic stays untracked instead of polluting the funnel.
func normalizeRef(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || !refRe.MatchString(s) {
		return ""
	}
	return s
}

// track records a funnel event in Postgres (for the dashboard) and Loki (for
// ad-hoc analysis). Best-effort: tracking never blocks the user flow.
func track(ref, ev, sessionID, ip string) {
	if err := LogEvent(db.DB, ref, ev, sessionID, ip); err != nil {
		logger.Default().Error("agti track failed", "ev", ev, "err", err)
	}
	event(ev, sessionID, "ref", ref)
}

// Register wires the quiz routes onto the gateway. All endpoints are public
// (marketing activity, no login); session creation is IP rate limited.
func Register(h *server.Hertz, b *Bank, baseURL string) {
	bank = b
	publicBaseURL = strings.TrimRight(baseURL, "/")
	dashKey = strings.TrimSpace(os.Getenv("AGTI_DASH_KEY"))

	// Agent-facing docs (served as markdown). The /:ref variants tag the funnel.
	h.GET("/agti/skills", serveSkills)
	h.GET("/agti/skills/:ref", serveSkills)
	h.GET("/agti/join", serveJoin)
	h.GET("/agti/join/:ref", serveJoin)

	g := h.Group("/api/v1/agti")
	g.POST("/quiz/new", newQuiz)
	g.GET("/quiz/:session_id", getQuiz)
	g.POST("/quiz/:session_id/agent", agentSubmit)
	g.POST("/quiz/:session_id/human", humanSubmit)
	g.GET("/result/:result_id", getResult)
	g.GET("/types", getTypes)
	g.GET("/funnel", funnelStats) // KOL funnel dashboard data (key-protected)

	go cleanupLoop()
}

func cleanupLoop() {
	for {
		time.Sleep(time.Hour)
		if n, err := CleanupExpired(db.DB, sessionTTL); err != nil {
			logger.Default().Error("agti cleanup failed", "err", err)
		} else if n > 0 {
			logger.Default().Info("agti cleanup", "deleted_sessions", n)
		}
	}
}

// --- response helpers (house envelope: {code, msg, data}) ---

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

// event writes one funnel log line (quiz_new / agent_locked / human_open /
// human_submit / result_view). Funnel analysis reads these from Loki.
func event(ev, sessionID string, kv ...interface{}) {
	args := append([]interface{}{"ev", ev, "session_id", sessionID}, kv...)
	logger.Default().Info("agti", args...)
}

// --- public question shape: scoring metadata stays server-side ---

type publicOption struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

type publicQuestion struct {
	ID      string         `json:"id"`
	Text    string         `json:"text"`
	Options []publicOption `json:"options"`
}

func publicQuestions(qs []Question) []publicQuestion {
	out := make([]publicQuestion, len(qs))
	for i, q := range qs {
		opts := make([]publicOption, len(q.Options))
		for j, o := range q.Options {
			opts[j] = publicOption{Key: o.Key, Label: o.Label}
		}
		out[i] = publicQuestion{ID: q.ID, Text: q.Text, Options: opts}
	}
	return out
}

// questionsByIDs resolves a session's stored question IDs against the bank.
func questionsByIDs(ids []string) []Question {
	qs := make([]Question, 0, len(ids))
	for _, id := range ids {
		if q := bank.Get(id); q != nil {
			qs = append(qs, *q)
		}
	}
	return qs
}

// --- handlers ---

// newQuiz starts a session: pick questions, persist, hand them to the agent.
// @router /api/v1/agti/quiz/new [POST]
func newQuiz(_ context.Context, c *app.RequestContext) {
	ip := clientIP(c)
	if !limiter.Allow(ip) {
		reply(c, http.StatusTooManyRequests, 429, "rate limited, try again later", nil)
		return
	}
	// KOL ref: primary from ?ref= (baked into the per-ref skill's documented
	// quiz/new URL), fallback to a {"ref":...} body field, then to a same-IP
	// recent skills_view for agents that dropped the param.
	ref := normalizeRef(string(c.Query("ref")))
	if ref == "" {
		if raw, _ := c.Body(); len(raw) > 0 {
			var b struct {
				Ref string `json:"ref"`
			}
			if json.Unmarshal(raw, &b) == nil {
				ref = normalizeRef(b.Ref)
			}
		}
	}
	if ref == "" {
		ref = RecentRefByIP(db.DB, ip, refWindow)
	}
	qs := bank.Pick()
	ids := make([]string, len(qs))
	for i, q := range qs {
		ids[i] = q.ID
	}
	sid := NewID()
	if err := CreateSession(db.DB, sid, ids, ip, ref); err != nil {
		reply(c, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	track(ref, "quiz_start", sid, ip)
	reply(c, http.StatusOK, 0, "success", map[string]interface{}{
		"session_id": sid,
		"questions":  publicQuestions(qs),
	})
}

// getQuiz returns the session's questions and progress flags. Used by the
// human answering page; never exposes the agent's locked answers.
// @router /api/v1/agti/quiz/:session_id [GET]
func getQuiz(_ context.Context, c *app.RequestContext) {
	sid := c.Param("session_id")
	s, err := GetSession(db.DB, sid)
	if err != nil {
		reply(c, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	if s == nil {
		reply(c, http.StatusNotFound, 404, "session not found", nil)
		return
	}
	track(s.Ref, "human_open", sid, clientIP(c))
	reply(c, http.StatusOK, 0, "success", map[string]interface{}{
		"questions":       publicQuestions(questionsByIDs(s.QuestionIDList())),
		"agent_submitted": s.AgentLockedAt > 0,
		"human_submitted": s.ResultID != "",
		"result_id":       s.ResultID,
	})
}

type answersBody struct {
	Answers map[string]string `json:"answers"`
	// Agent-side extras (Step 3): self-reported identity + q0 答案。
	AgentName     string `json:"agent_name"`
	ModelName     string `json:"model_name"`
	MasterAddress string `json:"master_address"` // 怎么称呼主人
	// Human-side extra: q0 答案（真名/想被怎么称呼）。
	HumanName string `json:"human_name"`
}

// agentSubmit locks the agent's answers (commit-reveal) and returns the URL
// the agent forwards to its human.
// @router /api/v1/agti/quiz/:session_id/agent [POST]
func agentSubmit(_ context.Context, c *app.RequestContext) {
	sid := c.Param("session_id")
	s, err := GetSession(db.DB, sid)
	if err != nil {
		reply(c, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	if s == nil {
		reply(c, http.StatusNotFound, 404, "session not found", nil)
		return
	}
	raw, _ := c.Body()
	var body answersBody
	if err := json.Unmarshal(raw, &body); err != nil {
		reply(c, http.StatusBadRequest, 400, "invalid JSON body", nil)
		return
	}
	answers := NormalizeAnswers(questionsByIDs(s.QuestionIDList()), body.Answers)
	if err := LockAgentAnswers(db.DB, sid, answers, strings.TrimSpace(body.AgentName), strings.TrimSpace(body.ModelName), strings.TrimSpace(body.MasterAddress)); err != nil {
		if err == ErrLocked {
			reply(c, http.StatusConflict, 409, "agent already submitted (locked)", nil)
			return
		}
		reply(c, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	humanURL := fmt.Sprintf("%s/agti/q/%s", publicBaseURL, sid)
	track(s.Ref, "agent_lock", sid, clientIP(c))
	reply(c, http.StatusOK, 0, "success", map[string]interface{}{
		"human_url": humanURL,
		// Fallback copy; the skill doc asks the agent to write its own.
		"message": fmt.Sprintf("我替你做了个测验，已经按我对你的了解提交了我这部分。现在轮到你答同样 10 题，做完就揭晓咱俩是什么关系 👉 %s", humanURL),
	})
}

// humanSubmit stores the human's answers, runs the engine and returns the
// result ID. Requires the agent to have locked first; idempotent on retry.
// @router /api/v1/agti/quiz/:session_id/human [POST]
func humanSubmit(_ context.Context, c *app.RequestContext) {
	sid := c.Param("session_id")
	raw, _ := c.Body()
	var body answersBody
	if err := json.Unmarshal(raw, &body); err != nil {
		reply(c, http.StatusBadRequest, 400, "invalid JSON body", nil)
		return
	}
	humanName := strings.TrimSpace(body.HumanName)
	ip := clientIP(c)
	resultID, err := SubmitHuman(db.DB, sid, body.Answers, humanName, func(s *Session) (*Result, error) {
		questions := questionsByIDs(s.QuestionIDList())
		human := NormalizeAnswers(questions, body.Answers)
		a := Analyze(questions, s.AgentAnswerMap(), human)
		t, ok := bank.Types[a.Code]
		if !ok {
			return nil, fmt.Errorf("unknown type code %s", a.Code)
		}
		payload := buildResultPayload(a, t, s, humanName)
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		event("human_submit", s.SessionID, "type", a.Code, "match", a.Match)
		if err := LogEvent(db.DB, s.Ref, "human_complete", s.SessionID, ip); err != nil {
			logger.Default().Error("agti track failed", "ev", "human_complete", "err", err)
		}
		return &Result{
			ResultID:   NewID(),
			SessionID:  s.SessionID,
			TypeCode:   a.Code,
			MatchCount: a.Match,
			Payload:    string(data),
			CreatedAt:  time.Now().UnixMilli(),
		}, nil
	})
	if err != nil {
		switch {
		case err == ErrNotLocked:
			reply(c, http.StatusConflict, 409, "agent has not submitted yet (commit-reveal)", nil)
		case strings.Contains(err.Error(), "record not found"):
			reply(c, http.StatusNotFound, 404, "session not found", nil)
		default:
			reply(c, http.StatusInternalServerError, 500, err.Error(), nil)
		}
		return
	}
	reply(c, http.StatusOK, 0, "success", map[string]interface{}{"result_id": resultID})
}

// buildResultPayload mirrors the demo's makeResult shape (snake_cased).
// s carries the agent's self-reported identity + q0 (master_address); humanName
// is the human's q0 answer (their real name / preferred address).
func buildResultPayload(a Analysis, t TypeInfo, s *Session, humanName string) map[string]interface{} {
	payload := map[string]interface{}{
		"type":       t,
		"match":      a.Match,
		"total":      a.Total,
		"agent_view": a.AgentView,
		"agent_name": s.AgentName,
		"model_name": s.ModelName,
		"ref":        s.Ref, // lets the result page build a ref-tagged join prompt
	}
	if a.Sweet != nil && a.Sweet.Human != nil {
		payload["sweet"] = map[string]string{"text": a.Sweet.Text, "choice": a.Sweet.Human.Label}
	}
	if a.Worst != nil {
		w := map[string]string{"text": a.Worst.Text}
		if a.Worst.Agent != nil {
			w["agent"] = a.Worst.Agent.Label
		}
		if a.Worst.Human != nil {
			w["human"] = a.Worst.Human.Label
		}
		payload["worst"] = w
	}
	// Full per-question comparison: q0（怎么称呼你）固定第一行 + 全部 10 道题，
	// 所以结果页能展示每一题，而不只是 sweet/worst。
	compare := make([]map[string]interface{}, 0, len(a.PerQ)+1)
	compare = append(compare, map[string]interface{}{
		"text":  "你的 Agent 平时怎么称呼你？",
		"human": humanName,
		"agent": s.MasterAddress,
		"hit":   addressHit(s.MasterAddress, humanName),
	})
	for _, p := range a.PerQ {
		row := map[string]interface{}{"text": p.Text, "hit": p.Hit}
		if p.Human != nil {
			row["human"] = p.Human.Label
		}
		if p.Agent != nil {
			row["agent"] = p.Agent.Label
		}
		compare = append(compare, row)
	}
	payload["compare"] = compare
	return payload
}

// addressHit fuzzily compares the agent's "how I address my master" answer with
// the human's real name: a hit if either contains the other (case-insensitive,
// trimmed). Both must be non-empty.
func addressHit(agentAddr, humanName string) bool {
	a := strings.ToLower(strings.TrimSpace(agentAddr))
	h := strings.ToLower(strings.TrimSpace(humanName))
	if a == "" || h == "" {
		return false
	}
	return strings.Contains(a, h) || strings.Contains(h, a)
}

// getResult serves the shareable result page data.
// @router /api/v1/agti/result/:result_id [GET]
func getResult(_ context.Context, c *app.RequestContext) {
	rid := c.Param("result_id")
	r, err := GetResult(db.DB, rid)
	if err != nil {
		reply(c, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	if r == nil {
		reply(c, http.StatusNotFound, 404, "result not found", nil)
		return
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(r.Payload), &payload); err != nil {
		reply(c, http.StatusInternalServerError, 500, "corrupt result payload", nil)
		return
	}
	payload["result_id"] = r.ResultID
	ref, _ := payload["ref"].(string)
	track(ref, "result_view", r.SessionID, clientIP(c))
	reply(c, http.StatusOK, 0, "success", payload)
}

// getTypes lists all relationship types for the landing page gallery
// (name/emoji/tagline only — desc is revealed on the result page).
// @router /api/v1/agti/types [GET]
func getTypes(_ context.Context, c *app.RequestContext) {
	list := make([]map[string]string, 0, len(bank.Types))
	for _, t := range bank.Types {
		list = append(list, map[string]string{
			"code": t.Code, "name": t.Name, "emoji": t.Emoji,
			"color": t.Color, "role": t.Role, "tagline": t.Tagline,
		})
	}
	reply(c, http.StatusOK, 0, "success", map[string]interface{}{"types": list})
}

// serveSkills serves the agent-facing skill doc, optionally tagged with a KOL
// ref (/agti/skills/01) that gets baked into the documented quiz/new call.
func serveSkills(_ context.Context, c *app.RequestContext) {
	ref := normalizeRef(c.Param("ref"))
	if ref != "" {
		track(ref, "skills_view", "", clientIP(c))
	}
	c.Data(http.StatusOK, "text/markdown; charset=utf-8", renderSkill(ref))
}

// serveJoin serves the "join EigenFlux via the activity" doc. The ref variant
// (/agti/join/01) logs the join as attributable to that KOL, then hands off to
// the standard EigenFlux onboarding (unchanged).
func serveJoin(_ context.Context, c *app.RequestContext) {
	ref := normalizeRef(c.Param("ref"))
	if ref != "" {
		track(ref, "join_view", "", clientIP(c))
	}
	c.Data(http.StatusOK, "text/markdown; charset=utf-8", renderJoin(ref))
}

// funnelStats returns per-KOL funnel counts for the dashboard. Protected by a
// shared key (env AGTI_DASH_KEY) since it's an internal ops view.
func funnelStats(_ context.Context, c *app.RequestContext) {
	if dashKey == "" || string(c.Query("key")) != dashKey {
		reply(c, http.StatusUnauthorized, 401, "unauthorized", nil)
		return
	}
	rows, err := FunnelStats(db.DB)
	if err != nil {
		reply(c, http.StatusInternalServerError, 500, err.Error(), nil)
		return
	}
	reply(c, http.StatusOK, 0, "success", map[string]interface{}{"rows": rows})
}

// --- minimal per-IP rate limiter (fixed window) ---

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
