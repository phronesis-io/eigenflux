// Package agentcardapi serves the Agent Card read/write endpoints. Routes are
// hand-registered (like api/install and the settings routes) so no IDL/router
// regen is needed, and every response is a whitelist DTO assembled here —
// never a raw agent_cards row.
package agentcardapi

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"eigenflux_server/api/middleware"
	"eigenflux_server/pkg/activity"
	"eigenflux_server/pkg/agentcard"
	"eigenflux_server/pkg/agentidentity"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/metrics"
	"eigenflux_server/pkg/mq"
	pmdal "eigenflux_server/rpc/pm/dal"
	profiledal "eigenflux_server/rpc/profile/dal"
)

const (
	// maxFieldsPerUpdate bounds one PUT /agents/me/profile/fields request.
	maxFieldsPerUpdate  = 32
	maxProfileBodyBytes = 128 << 10
	// publicCardRateLimit / publicCardRateWindow throttle per-viewer reads of
	// other agents' cards (first cross-agent profile read endpoint).
	publicCardRateLimit  = 120
	publicCardRateWindow = time.Minute

	// Automated refresh is intentionally cheap for cooperative clients, but
	// these authenticated endpoints still need server-side abuse bounds: local
	// CLI timestamps are under the caller's control and can be bypassed.
	refreshContextRateLimit  = 60
	refreshContextRateWindow = time.Minute
	profileWriteMinuteLimit  = 10
	profileWriteMinuteWindow = time.Minute
	// Twenty writes/day is far above the expected one daily refresh while
	// keeping the fleet-wide worst case below the cleanup job's daily capacity.
	profileWriteDailyLimit  = 20
	profileWriteDailyWindow = 24 * time.Hour
)

var fixedWindowCounterScript = redis.NewScript(`
local t = redis.call("TIME")
local now = t[1] * 1000 + math.floor(t[2] / 1000)
local window = tonumber(ARGV[1])
local limit = tonumber(ARGV[2])
redis.call("ZREMRANGEBYSCORE", KEYS[1], "-inf", now - window)
local count = redis.call("ZCARD", KEYS[1])
if count >= limit then
  return {0, count}
end
local seq = redis.call("INCR", KEYS[2])
redis.call("PEXPIRE", KEYS[2], window + 1000)
redis.call("ZADD", KEYS[1], now, tostring(now) .. ":" .. tostring(seq))
redis.call("PEXPIRE", KEYS[1], window + 1000)
return {1, count + 1}
`)

var profileWriteCounterScript = redis.NewScript(`
local t = redis.call("TIME")
local now = t[1] * 1000 + math.floor(t[2] / 1000)
local minute_window = tonumber(ARGV[1])
local day_window = tonumber(ARGV[2])
local minute_limit = tonumber(ARGV[3])
local day_limit = tonumber(ARGV[4])
redis.call("ZREMRANGEBYSCORE", KEYS[1], "-inf", now - minute_window)
redis.call("ZREMRANGEBYSCORE", KEYS[2], "-inf", now - day_window)
local minute_count = redis.call("ZCARD", KEYS[1])
local day_count = redis.call("ZCARD", KEYS[2])
if minute_count >= minute_limit or day_count >= day_limit then
  return {0, minute_count, day_count}
end
local seq = redis.call("INCR", KEYS[3])
local member = tostring(now) .. ":" .. tostring(seq)
redis.call("PEXPIRE", KEYS[3], day_window + 1000)
redis.call("ZADD", KEYS[1], now, member)
redis.call("ZADD", KEYS[2], now, member)
redis.call("PEXPIRE", KEYS[1], minute_window + 1000)
redis.call("PEXPIRE", KEYS[2], day_window + 1000)
return {1, minute_count + 1, day_count + 1}
`)

// Register wires the Agent Card routes onto the gateway.
func Register(h *server.Hertz) {
	h.GET("/api/v1/agents/me/card", middleware.AuthMiddleware(), GetMyCard)
	h.GET("/api/v1/agents/me/card/refresh-context", middleware.AuthMiddleware(), GetRefreshContext)
	h.PUT("/api/v1/agents/me/profile/fields", middleware.AuthMiddleware(), PutProfileFields)
	h.GET("/api/v1/agents/:agent_id/card", middleware.AuthMiddleware(), GetPublicCard)
}

// RegisterPublic wires the anonymous, stable Agent Card routes independently
// from Console V2. Public Agent identity must remain reachable when the
// authenticated console is disabled or being rolled back.
func RegisterPublic(h *server.Hertz) {
	h.GET("/api/v2/public/agents/by-id/:agent_id/card", GetSharedPublicCardByAgentID)
	h.GET("/api/v2/public/agents/:short_id/card", GetSharedPublicCard)
}

func respond(c *app.RequestContext, status int, code int, msg string, data map[string]interface{}) {
	resp := map[string]interface{}{"code": code, "msg": msg}
	if data != nil {
		resp["data"] = data
	}
	c.JSON(status, resp)
}

func callerAgentID(c *app.RequestContext) (int64, bool) {
	v, ok := c.Get("agent_id")
	if !ok {
		respond(c, http.StatusUnauthorized, 401, "invalid or expired token", nil)
		return 0, false
	}
	id, ok := v.(int64)
	if !ok {
		respond(c, http.StatusUnauthorized, 401, "invalid or expired token", nil)
		return 0, false
	}
	return id, true
}

// loadCardRebuildOnMiss returns the projection row, rebuilding it once when
// the agent predates the projection backfill.
func loadCardRebuildOnMiss(ctx context.Context, agentID int64) (*profiledal.AgentCard, error) {
	card, err := profiledal.GetAgentCard(db.DB, agentID)
	if err == nil {
		if card.SchemaVersion == agentcard.SchemaVersion {
			return card, nil
		}
		if rerr := agentcard.RebuildOnMiss(ctx, db.DB, mq.RDB, agentID); rerr != nil {
			return nil, rerr
		}
		return profiledal.GetAgentCard(db.DB, agentID)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if rerr := agentcard.RebuildOnMiss(ctx, db.DB, mq.RDB, agentID); rerr != nil {
		return nil, rerr
	}
	return profiledal.GetAgentCard(db.DB, agentID)
}

// GetMyCard returns the caller's full card (public + private projections).
// @Summary Get the authenticated agent's full Agent Card
// @Tags Agent Card
// @Produce json
// @Security BearerAuth
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/agents/me/card [get]
func GetMyCard(ctx context.Context, c *app.RequestContext) {
	agentID, ok := callerAgentID(c)
	if !ok {
		return
	}
	card, err := loadCardRebuildOnMiss(ctx, agentID)
	if err != nil {
		logger.Ctx(ctx).Error("GetMyCard failed", "agentID", agentID, "err", err)
		respond(c, http.StatusInternalServerError, 500, "failed to load card", nil)
		return
	}
	publicCard := overlayPublicMetadata(ctx, card.PublicCard, agentID)
	respond(c, http.StatusOK, 0, "success", map[string]interface{}{
		"public":       publicCard,
		"private":      json.RawMessage(card.PrivateCard),
		"card_version": card.CardVersion,
		"generated_at": card.GeneratedAt,
	})
}

const agentCardPageSnapshotSQL = `SELECT agent.agent_name,
		agent.short_id,
		agent.bio,
		COALESCE(profile.profile_version, 0) AS profile_version,
		COALESCE(profile.profile_data, '{}'::jsonb)::text AS profile_data,
		COALESCE(goal.goal_text, '') AS network_goal,
		COALESCE(intents.items, '[]'::jsonb)::text AS intent_actions,
		COALESCE(membership.member_no, 0) AS member_no
	FROM agents AS agent
	LEFT JOIN agent_profiles AS profile ON profile.agent_id = agent.agent_id
	LEFT JOIN agent_network_memberships AS membership ON membership.agent_id = agent.agent_id
	LEFT JOIN agent_network_goals AS goal
	  ON goal.agent_id = agent.agent_id AND goal.status = 'active'
	LEFT JOIN LATERAL (
		SELECT jsonb_agg(jsonb_build_object(
			'intent_id', intent.intent_id::text,
			'watch_for', intent.watch_for
		) ORDER BY intent.priority DESC, intent.intent_id) AS items
		FROM agent_intent_actions AS intent
		WHERE intent.agent_id = agent.agent_id AND intent.status = 'active'
	) AS intents ON TRUE
	WHERE agent.agent_id = ?`

type agentCardPageSnapshot struct {
	AgentName      string `gorm:"column:agent_name"`
	ShortID        string `gorm:"column:short_id"`
	Bio            string `gorm:"column:bio"`
	ProfileVersion int64  `gorm:"column:profile_version"`
	ProfileData    string `gorm:"column:profile_data"`
	NetworkGoal    string `gorm:"column:network_goal"`
	IntentActions  string `gorm:"column:intent_actions"`
	MemberNo       int64  `gorm:"column:member_no"`
}

// GetMyCardPage is the Console V2 Agent Card first-paint read model. It keeps
// the browser on one authenticated request and deliberately does not load the
// append-only field-change history required by the automated refresh flow.
func GetMyCardPage(ctx context.Context, c *app.RequestContext) {
	agentID, ok := callerAgentID(c)
	if !ok {
		return
	}
	card, err := loadCardRebuildOnMiss(ctx, agentID)
	if err != nil {
		logger.Ctx(ctx).Error("GetMyCardPage card read failed", "agentID", agentID, "err", err)
		respond(c, http.StatusInternalServerError, 500, "failed to load card page", nil)
		return
	}

	var snapshot agentCardPageSnapshot
	result := db.DB.Raw(agentCardPageSnapshotSQL, agentID).Scan(&snapshot)
	if result.Error != nil || snapshot.AgentName == "" {
		logger.Ctx(ctx).Error("GetMyCardPage snapshot read failed", "agentID", agentID, "err", result.Error)
		respond(c, http.StatusInternalServerError, 500, "failed to load card page", nil)
		return
	}

	profileData := map[string]json.RawMessage{}
	if err := json.Unmarshal([]byte(snapshot.ProfileData), &profileData); err != nil {
		logger.Ctx(ctx).Error("GetMyCardPage profile decode failed", "agentID", agentID, "err", err)
		respond(c, http.StatusInternalServerError, 500, "failed to load card page", nil)
		return
	}
	currentValues := currentAgentCardValues(snapshot.AgentName, snapshot.Bio, profileData)
	publicCard := overlayCurrentLastActive(ctx, card.PublicCard, agentID)
	publicCard = overlayPublicIdentity(publicCard, snapshot.MemberNo, snapshot.ShortID, snapshot.AgentName)

	respond(c, http.StatusOK, 0, "success", map[string]interface{}{
		"card": map[string]interface{}{
			"public":       publicCard,
			"private":      json.RawMessage(card.PrivateCard),
			"card_version": card.CardVersion,
			"generated_at": card.GeneratedAt,
		},
		"profile_version": snapshot.ProfileVersion,
		"current_values":  currentValues,
		"network_goal":    snapshot.NetworkGoal,
		"intent_actions":  json.RawMessage(snapshot.IntentActions),
	})
}

func currentAgentCardValues(agentName, bio string, profileData map[string]json.RawMessage) map[string]interface{} {
	values := make(map[string]interface{}, len(agentcard.EditableFields))
	for _, spec := range agentcard.EditableFields {
		switch spec.Name {
		case "agent_name":
			values[spec.Name] = agentName
		case "agent_description":
			values[spec.Name] = bio
		default:
			raw, exists := profileData[spec.Name]
			if !exists {
				values[spec.Name] = nil
				continue
			}
			var value interface{}
			if err := json.Unmarshal(raw, &value); err != nil {
				values[spec.Name] = nil
				continue
			}
			values[spec.Name] = value
		}
	}
	return values
}

// GetPublicCard returns another agent's public card plus the viewer-relative
// relation block. Blocked pairs (either direction) get an indistinguishable
// 404 so blocking is not observable.
// @Summary Get an agent's public Agent Card
// @Tags Agent Card
// @Produce json
// @Security BearerAuth
// @Param agent_id path string true "Agent ID"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/agents/{agent_id}/card [get]
func GetPublicCard(ctx context.Context, c *app.RequestContext) {
	viewerID, ok := callerAgentID(c)
	if !ok {
		return
	}
	targetID, err := strconv.ParseInt(c.Param("agent_id"), 10, 64)
	if err != nil || targetID <= 0 {
		respond(c, http.StatusBadRequest, 400, "invalid agent_id", nil)
		return
	}

	allowed, rateErr := checkFixedWindow(ctx, mq.RDB, viewerID, "public-card", publicCardRateLimit, publicCardRateWindow, time.Now())
	if rateErr != nil {
		logger.Ctx(ctx).Error("public card rate limiter unavailable, failing closed", "viewerID", viewerID, "err", rateErr)
		c.Header("Retry-After", "60")
		respond(c, http.StatusServiceUnavailable, 503, "card reads are temporarily unavailable", nil)
		return
	}
	if !allowed {
		metrics.AgentShortIDLookupRateLimitedTotal.Inc()
		c.Header("Retry-After", "60")
		respond(c, http.StatusTooManyRequests, 429, "too many card reads, slow down", nil)
		return
	}

	if viewerID != targetID {
		// Fail closed: if the block lookup errors we cannot prove the pair is
		// unblocked, and serving the card anyway would leak through the shield.
		blocked, err := pmdal.IsBlocked(db.DB, viewerID, targetID)
		if err == nil && !blocked {
			blocked, err = pmdal.IsBlocked(db.DB, targetID, viewerID)
		}
		if err != nil {
			logger.Ctx(ctx).Error("GetPublicCard block check failed", "viewerID", viewerID, "targetID", targetID, "err", err)
			respond(c, http.StatusInternalServerError, 500, "failed to load card", nil)
			return
		}
		if blocked {
			respond(c, http.StatusNotFound, 404, "agent not found", nil)
			return
		}
	}

	card, err := loadCardRebuildOnMiss(ctx, targetID)
	if err != nil {
		if errors.Is(err, agentcard.ErrAgentNotFound) {
			respond(c, http.StatusNotFound, 404, "agent not found", nil)
			return
		}
		logger.Ctx(ctx).Error("GetPublicCard failed", "viewerID", viewerID, "targetID", targetID, "err", err)
		respond(c, http.StatusInternalServerError, 500, "failed to load card", nil)
		return
	}

	publicCard := overlayPublicMetadata(ctx, card.PublicCard, targetID)
	respond(c, http.StatusOK, 0, "success", map[string]interface{}{
		"card":               publicCard,
		"relation_to_viewer": relationToViewer(viewerID, targetID),
	})
}

// GetSharedPublicCard serves the stable anonymous Agent Card route. The URL
// accepts only the public short ID; numeric database IDs never appear in newly
// generated share links.
func GetSharedPublicCard(ctx context.Context, c *app.RequestContext) {
	if !allowAnonymousCardLookup(ctx, c) {
		return
	}
	shortID := c.Param("short_id")
	targetID, err := agentidentity.Lookup(ctx, db.DB, shortID)
	if err != nil {
		if errors.Is(err, agentidentity.ErrNotFound) {
			respond(c, http.StatusNotFound, 404, "agent not found", nil)
			return
		}
		logger.Ctx(ctx).Error("GetSharedPublicCard identity lookup failed", "shortID", shortID, "err", err)
		respond(c, http.StatusInternalServerError, 500, "failed to resolve agent", nil)
		return
	}
	serveSharedPublicCard(ctx, c, targetID, shortID)
}

// GetSharedPublicCardByAgentID resolves legacy numeric links. New clients must
// use GetSharedPublicCard and the short-ID share_path returned here.
func GetSharedPublicCardByAgentID(ctx context.Context, c *app.RequestContext) {
	if !allowAnonymousCardLookup(ctx, c) {
		return
	}
	targetID, err := strconv.ParseInt(c.Param("agent_id"), 10, 64)
	if err != nil || targetID <= 0 {
		respond(c, http.StatusNotFound, 404, "agent not found", nil)
		return
	}
	identity, err := agentidentity.Get(ctx, db.DB, targetID)
	if err != nil {
		respond(c, http.StatusNotFound, 404, "agent not found", nil)
		return
	}
	serveSharedPublicCard(ctx, c, targetID, identity.ShortID)
}

func serveSharedPublicCard(ctx context.Context, c *app.RequestContext, targetID int64, shortID string) {
	card, err := loadCardRebuildOnMiss(ctx, targetID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) || errors.Is(err, agentcard.ErrAgentNotFound) {
			respond(c, http.StatusNotFound, 404, "agent not found", nil)
			return
		}
		logger.Ctx(ctx).Error("GetSharedPublicCard failed", "targetID", targetID, "err", err)
		respond(c, http.StatusInternalServerError, 500, "failed to load card", nil)
		return
	}
	c.Header("Cache-Control", "public, max-age=60, stale-while-revalidate=300")
	c.Header("ETag", fmt.Sprintf("\"agent-card-%s-%d\"", shortID, card.PublicCardVersion))
	identity, err := agentidentity.Get(ctx, db.DB, targetID)
	if err != nil {
		respond(c, http.StatusNotFound, 404, "agent not found", nil)
		return
	}
	respond(c, http.StatusOK, 0, "success", map[string]interface{}{
		"card":                overlayPublicMetadata(ctx, card.PublicCard, targetID),
		"public_identity":     identity,
		"share_path":          "/agent/" + identity.ShortID,
		"public_card_version": card.PublicCardVersion,
		"generated_at":        card.PublicCardGeneratedAt,
	})
}

func allowAnonymousCardLookup(ctx context.Context, c *app.RequestContext) bool {
	digest := sha256.Sum256([]byte(c.ClientIP()))
	subject := int64(binary.BigEndian.Uint64(digest[:8]) & 0x7fffffffffffffff)
	allowed, err := checkFixedWindow(ctx, mq.RDB, subject, "public-card-anonymous", 120, time.Minute, time.Now())
	if err != nil {
		logger.Ctx(ctx).Error("anonymous public card rate limiter unavailable", "err", err)
		c.Header("Retry-After", "60")
		respond(c, http.StatusServiceUnavailable, 503, "card reads are temporarily unavailable", nil)
		return false
	}
	if !allowed {
		c.Header("Retry-After", "60")
		respond(c, http.StatusTooManyRequests, 429, "too many card reads, slow down", nil)
		return false
	}
	return true
}

func overlayPublicMetadata(ctx context.Context, raw string, agentID int64) json.RawMessage {
	card := overlayCurrentLastActive(ctx, raw, agentID)
	var metadata struct {
		MemberNo  int64  `gorm:"column:member_no"`
		ShortID   string `gorm:"column:short_id"`
		AgentName string `gorm:"column:agent_name"`
	}
	if err := db.DB.Raw(`SELECT COALESCE(m.member_no, 0) AS member_no, a.short_id, a.agent_name
		FROM agents a LEFT JOIN agent_network_memberships m ON m.agent_id = a.agent_id
		WHERE a.agent_id = ?`, agentID).Scan(&metadata).Error; err != nil {
		logger.Ctx(ctx).Warn("network member number unavailable", "agentID", agentID, "err", err)
	}
	return overlayPublicIdentity(card, metadata.MemberNo, metadata.ShortID, metadata.AgentName)
}

func overlayPublicIdentity(card json.RawMessage, memberNo int64, shortID, agentName string) json.RawMessage {
	var payload map[string]interface{}
	if err := json.Unmarshal(card, &payload); err != nil {
		return card
	}
	if memberNo > 0 {
		payload["network_member_no"] = memberNo
	}
	if agentidentity.ValidShortID(shortID) {
		payload["short_id"] = shortID
		payload["display_name"] = agentidentity.DisplayName(agentName, shortID)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return card
	}
	return json.RawMessage(encoded)
}

// overlayMemberNumber remains as the narrow compatibility helper used by
// existing projection tests and internal callers that do not yet carry a
// public identity.
func overlayMemberNumber(card json.RawMessage, memberNo int64) json.RawMessage {
	return overlayPublicIdentity(card, memberNo, "", "")
}

// overlayCurrentLastActive keeps the hot activity signal current without
// rebuilding the whole projection on every authenticated request. If Redis or
// JSON decoding is unavailable, the persisted card remains a safe fallback.
func overlayCurrentLastActive(ctx context.Context, raw string, agentID int64) json.RawMessage {
	lastActive, ok := agentcard.GetLastActive(ctx, mq.RDB, agentID)
	if !ok {
		return json.RawMessage(raw)
	}
	return overlayLastActive(raw, lastActive)
}

func overlayLastActive(raw string, lastActive int64) json.RawMessage {
	var card map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &card); err != nil {
		return json.RawMessage(raw)
	}
	card["last_active_at"] = lastActive
	b, err := json.Marshal(card)
	if err != nil {
		return json.RawMessage(raw)
	}
	return json.RawMessage(b)
}

// relationToViewer is viewer-relative and therefore computed at read time,
// never stored in the projection (a card row is per-agent; relations are
// per-pair).
func relationToViewer(viewerID, targetID int64) map[string]interface{} {
	if viewerID == targetID {
		return map[string]interface{}{"category": "self", "is_friend": false}
	}
	out := map[string]interface{}{"category": "non_friend", "is_friend": false}
	if isFriend, err := pmdal.IsFriend(db.DB, viewerID, targetID); err == nil && isFriend {
		out["category"] = "friend"
		out["is_friend"] = true
		if remarks, rerr := pmdal.GetRemarksByPeer(db.DB, viewerID, []int64{targetID}); rerr == nil {
			if remark, ok := remarks[targetID]; ok {
				out["remark"] = remark
			}
		}
	}
	return out
}

// allowPublicCardRead is a fixed-window Redis counter; fail-open on Redis
// errors (rate limiting must not take the endpoint down), but loudly — a
// silent fail-open would hide that the limiter is off.
func allowPublicCardRead(ctx context.Context, viewerID int64) bool {
	return allowFixedWindow(ctx, mq.RDB, viewerID, "public-card", publicCardRateLimit, publicCardRateWindow, time.Now())
}

// checkFixedWindow retains its historical name but applies an exact rolling
// window, using Redis TIME so gateway clock skew cannot split a caller's quota.
func checkFixedWindow(ctx context.Context, rdb *redis.Client, agentID int64, scope string, limit int64, window time.Duration, now time.Time) (bool, error) {
	_ = now // retained in the helper signature for deterministic legacy callers; Redis TIME is authoritative.
	if rdb == nil {
		return false, fmt.Errorf("redis client is nil")
	}
	windowMs := window.Milliseconds()
	if limit <= 0 || windowMs <= 0 {
		return false, fmt.Errorf("invalid rate limit: limit=%d window=%s", limit, window)
	}
	key := fmt.Sprintf("agentcard:rl:%s:%d", scope, agentID)
	seqKey := key + ":seq"
	result, err := fixedWindowCounterScript.Run(ctx, rdb, []string{key, seqKey}, windowMs, limit).Int64Slice()
	if err != nil {
		return false, err
	}
	if len(result) != 2 {
		return false, fmt.Errorf("unexpected rolling limiter response: %v", result)
	}
	return result[0] == 1, nil
}

// allowFixedWindow is the fail-open read wrapper. Rate limiting must not take
// card reads or refresh-context down when Redis is unavailable.
func allowFixedWindow(ctx context.Context, rdb *redis.Client, agentID int64, scope string, limit int64, window time.Duration, now time.Time) bool {
	allowed, err := checkFixedWindow(ctx, rdb, agentID, scope, limit, window, now)
	if err != nil {
		logger.Ctx(ctx).Warn("agent card rate limiter unavailable, failing open", "agentID", agentID, "scope", scope, "err", err)
		return true
	}
	return allowed
}

// CheckProfileWriteRate applies the same write quota to both the versioned
// fields endpoint and the legacy name/bio compatibility endpoint. Unlike
// reads, writes fail closed when Redis is unavailable: unbounded writes grow
// audit/WAL data and may enqueue costly profile reprocessing.
func CheckProfileWriteRate(ctx context.Context, agentID int64) (bool, error) {
	return checkProfileWriteRate(ctx, mq.RDB, agentID, time.Now())
}

func checkProfileWriteRate(ctx context.Context, rdb *redis.Client, agentID int64, now time.Time) (bool, error) {
	_ = now // Redis TIME is authoritative across gateway instances.
	if rdb == nil {
		return false, fmt.Errorf("redis client is nil")
	}
	minuteMs := profileWriteMinuteWindow.Milliseconds()
	dayMs := profileWriteDailyWindow.Milliseconds()
	minuteKey := fmt.Sprintf("agentcard:rl:profile-write-minute:%d", agentID)
	dayKey := fmt.Sprintf("agentcard:rl:profile-write-day:%d", agentID)
	seqKey := fmt.Sprintf("agentcard:rl:profile-write-seq:%d", agentID)
	counts, err := profileWriteCounterScript.Run(
		ctx, rdb, []string{minuteKey, dayKey, seqKey}, minuteMs, dayMs,
		profileWriteMinuteLimit, profileWriteDailyLimit,
	).Int64Slice()
	if err != nil {
		return false, err
	}
	if len(counts) != 3 {
		return false, fmt.Errorf("unexpected profile write limiter response: %v", counts)
	}
	return counts[0] == 1, nil
}

// GetRefreshContext gives the agent everything it needs before an automated
// refresh: current version, per-field current/previous value + last actor,
// and the protected paths it must not write.
// @Summary Get versioned Agent Card refresh context
// @Tags Agent Card
// @Produce json
// @Security BearerAuth
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/agents/me/card/refresh-context [get]
func GetRefreshContext(ctx context.Context, c *app.RequestContext) {
	agentID, ok := callerAgentID(c)
	if !ok {
		return
	}
	allowed, rateErr := checkFixedWindow(ctx, mq.RDB, agentID, "refresh-context", refreshContextRateLimit, refreshContextRateWindow, time.Now())
	if rateErr != nil {
		logger.Ctx(ctx).Error("refresh-context rate limiter unavailable, failing closed", "agentID", agentID, "err", rateErr)
		c.Header("Retry-After", "60")
		respond(c, http.StatusServiceUnavailable, 503, "profile refresh context is temporarily unavailable", nil)
		return
	}
	if !allowed {
		c.Header("Retry-After", "60")
		respond(c, http.StatusTooManyRequests, 429, "too many profile refresh reads, slow down", nil)
		return
	}
	type lastChange struct {
		prevValue json.RawMessage
		updatedAt int64
		actorType string
	}
	var agent *profiledal.Agent
	var version int64
	var profileData map[string]json.RawMessage
	lastByPath := map[string]lastChange{}
	err := db.DB.Transaction(func(tx *gorm.DB) error {
		var readErr error
		agent, readErr = profiledal.GetAgentByID(tx, agentID)
		if readErr != nil {
			return readErr
		}
		version, profileData, readErr = profiledal.GetProfileVersionAndData(tx, agentID)
		if readErr != nil {
			return readErr
		}
		latestChanges, readErr := profiledal.ListLatestProfileFieldChanges(tx, agentID)
		if readErr != nil {
			return readErr
		}
		for _, change := range latestChanges {
			prev := json.RawMessage(nil)
			if change.PreviousValue != "" && change.PreviousValue != "null" {
				prev = json.RawMessage(change.PreviousValue)
			}
			lastByPath[change.Path] = lastChange{
				prevValue: prev,
				updatedAt: change.UpdatedAt,
				actorType: change.ActorType,
			}
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			respond(c, http.StatusNotFound, 404, "agent not found", nil)
			return
		}
		logger.Ctx(ctx).Error("GetRefreshContext snapshot read failed", "agentID", agentID, "err", err)
		respond(c, http.StatusInternalServerError, 500, "failed to load profile refresh context", nil)
		return
	}

	editable := map[string]interface{}{}
	for _, spec := range agentcard.EditableFields {
		var current interface{}
		switch spec.Name {
		case "agent_name":
			current = agent.AgentName
		case "agent_description":
			current = agent.Bio
		default:
			if raw, ok := profileData[spec.Name]; ok {
				current = json.RawMessage(raw)
			}
		}
		entry := map[string]interface{}{
			"current_value": current,
			"kind":          spec.Kind,
			"public":        spec.Public,
		}
		if lc, ok := lastByPath[spec.Name]; ok {
			if lc.prevValue != nil {
				entry["previous_value"] = lc.prevValue
			}
			entry["last_updated_at"] = lc.updatedAt
			entry["last_updated_by"] = lc.actorType
		}
		editable[spec.Name] = entry
	}

	respond(c, http.StatusOK, 0, "success", map[string]interface{}{
		"profile_version": version,
		"editable_fields": editable,
		"protected_paths": agentcard.ProtectedPaths,
	})
}

type ProfileFieldsReq struct {
	ExpectedVersion *int64                     `json:"expected_version"`
	Updates         map[string]json.RawMessage `json:"updates"`
	Source          string                     `json:"source"`
	Reason          string                     `json:"reason"`
}

// PutProfileFields is the field-level versioned profile write. All writes go
// to the fact tables (agents / agent_profiles.profile_data); the card is
// rebuilt asynchronously. actor_type is derived from the credential (agent
// token ⇒ "agent"), never trusted from the request body.
// @Summary Apply a versioned field-level profile patch
// @Tags Agent Card
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param body body ProfileFieldsReq true "Expected version and changed fields"
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/agents/me/profile/fields [put]
func PutProfileFields(ctx context.Context, c *app.RequestContext) {
	agentID, ok := callerAgentID(c)
	if !ok {
		return
	}
	body, berr := c.Body()
	if berr != nil {
		respond(c, http.StatusBadRequest, 400, "failed to read request body", nil)
		return
	}
	if len(body) > maxProfileBodyBytes {
		respond(c, http.StatusRequestEntityTooLarge, 413, fmt.Sprintf("profile update body exceeds %d bytes", maxProfileBodyBytes), nil)
		return
	}
	var req ProfileFieldsReq
	if err := json.Unmarshal(body, &req); err != nil {
		respond(c, http.StatusBadRequest, 400, "invalid JSON body", nil)
		return
	}
	if req.ExpectedVersion == nil {
		respond(c, http.StatusBadRequest, 400, "expected_version is required (fetch it from /agents/me/card/refresh-context)", nil)
		return
	}
	if len(req.Updates) == 0 {
		respond(c, http.StatusBadRequest, 400, "updates must contain at least one field", nil)
		return
	}
	if len(req.Updates) > maxFieldsPerUpdate {
		respond(c, http.StatusBadRequest, 400, fmt.Sprintf("too many fields in one update (max %d)", maxFieldsPerUpdate), nil)
		return
	}

	protected := map[string]bool{}
	for _, p := range agentcard.ProtectedPaths {
		protected[p] = true
	}

	// Validate everything before touching the DB.
	agentsUpdates := map[string]interface{}{}
	pdMerge := map[string]json.RawMessage{}
	changedPaths := make([]string, 0, len(req.Updates))
	newValues := map[string]json.RawMessage{}
	for name, raw := range req.Updates {
		if protected[name] {
			respond(c, http.StatusForbidden, 403, fmt.Sprintf("field %q is system-owned and cannot be written", name), nil)
			return
		}
		spec, known := agentcard.LookupField(name)
		if !known {
			respond(c, http.StatusBadRequest, 400, fmt.Sprintf("unknown field %q", name), nil)
			return
		}
		val, verr := agentcard.ValidateValue(spec, raw)
		if verr != nil {
			respond(c, http.StatusUnprocessableEntity, 422, verr.Error(), nil)
			return
		}
		if req.Source == "console_v2" {
			if verr := agentcard.ValidateConsoleV2Value(name, val); verr != nil {
				respond(c, http.StatusUnprocessableEntity, 422, verr.Error(), nil)
				return
			}
		}
		if perr := agentcard.ValidatePublicContent(spec, val); perr != nil {
			respond(c, http.StatusUnprocessableEntity, 422, perr.Error(), nil)
			return
		}
		normalized, merr := json.Marshal(val)
		if merr != nil {
			respond(c, http.StatusInternalServerError, 500, merr.Error(), nil)
			return
		}
		switch spec.Storage {
		case agentcard.StorageAgents:
			if name == "agent_name" {
				s := val.(string)
				if s == "" {
					respond(c, http.StatusUnprocessableEntity, 422, "agent_name cannot be empty", nil)
					return
				}
				agentsUpdates["agent_name"] = s
			} else { // agent_description ⇒ legacy agents.bio
				agentsUpdates["bio"] = val.(string)
			}
		case agentcard.StorageProfileData:
			pdMerge[name] = json.RawMessage(normalized)
		}
		changedPaths = append(changedPaths, name)
		newValues[name] = json.RawMessage(normalized)
	}
	// Invalid JSON and invalid fields never consume the authenticated request
	// quota. Conflicts still do: they reach the protected DB path and a caller
	// can otherwise use stale versions as an unbounded read-amplification loop.
	allowed, rateErr := CheckProfileWriteRate(ctx, agentID)
	if rateErr != nil {
		logger.Ctx(ctx).Error("profile write rate limiter unavailable, failing closed", "agentID", agentID, "err", rateErr)
		c.Header("Retry-After", "60")
		respond(c, http.StatusServiceUnavailable, 503, "profile updates are temporarily unavailable", nil)
		return
	}
	if !allowed {
		c.Header("Retry-After", "60")
		respond(c, http.StatusTooManyRequests, 429, "too many profile updates, slow down", nil)
		return
	}

	var newVersion int64
	var conflict bool
	var bioChanged bool
	var nameChanged bool
	var noChanges bool
	err := db.DB.Transaction(func(tx *gorm.DB) error {
		// Lock agents first on every profile writer. The legacy RPC path uses
		// the same order; taking agent_profiles first here would recreate the
		// AB-BA deadlock this endpoint was designed to avoid.
		agent, err := profiledal.GetAgentByIDForUpdate(tx, agentID)
		if err != nil {
			return err
		}
		if err := profiledal.EnsureAgentProfileRow(tx, agentID); err != nil {
			return err
		}
		currentVersion, prevData, err := profiledal.GetProfileVersionAndData(tx, agentID)
		if err != nil {
			return err
		}
		if currentVersion != *req.ExpectedVersion {
			conflict = true
			return errVersionConflict
		}

		// Filter semantic no-ops before touching facts or audit state. Repeating
		// a human-edited value must not relabel its latest actor as "agent".
		actualPaths := make([]string, 0, len(changedPaths))
		actualAgentUpdates := map[string]interface{}{}
		actualPDMerge := map[string]json.RawMessage{}
		actualNewValues := map[string]json.RawMessage{}
		prevValues := map[string]json.RawMessage{}
		for _, p := range changedPaths {
			switch p {
			case "agent_name":
				if agentsUpdates["agent_name"].(string) == agent.AgentName {
					continue
				}
				b, _ := json.Marshal(agent.AgentName)
				prevValues[p] = b
				actualAgentUpdates["agent_name"] = agentsUpdates["agent_name"]
				actualAgentUpdates["agent_name_en"] = ""
				nameChanged = true
			case "agent_description":
				if agentsUpdates["bio"].(string) == agent.Bio {
					continue
				}
				b, _ := json.Marshal(agent.Bio)
				prevValues[p] = b
				actualAgentUpdates["bio"] = agentsUpdates["bio"]
			default:
				if raw, ok := prevData[p]; ok {
					if jsonValuesEqual(raw, newValues[p]) {
						continue
					}
					prevValues[p] = raw
				}
				actualPDMerge[p] = pdMerge[p]
			}
			actualPaths = append(actualPaths, p)
			actualNewValues[p] = newValues[p]
		}
		changedPaths = actualPaths
		agentsUpdates = actualAgentUpdates
		pdMerge = actualPDMerge
		newValues = actualNewValues
		if len(changedPaths) == 0 {
			newVersion = currentVersion
			noChanges = true
			return nil
		}

		// Row-lock order MUST match the legacy UpdateProfile transaction
		// (agents first, then agent_profiles) — the two paths run concurrently
		// and opposite orders would AB-BA deadlock. A version conflict below
		// rolls this agents write back with the rest of the transaction.
		if len(agentsUpdates) > 0 {
			if err := profiledal.UpdateAgentFields(tx, agentID, agentsUpdates); err != nil {
				return err
			}
			if nb, ok := agentsUpdates["bio"]; ok && nb.(string) != agent.Bio {
				bioChanged = true
				// Keep the legacy bio audit trail alive during the compat window.
				if err := profiledal.InsertBioHistory(tx, agentID, agent.Bio, nb.(string), req.Source, req.Reason); err != nil {
					return err
				}
			}
		}

		v, conflicted, err := profiledal.ApplyVersionedProfileDataUpdate(tx, agentID, *req.ExpectedVersion, pdMerge)
		if err != nil {
			return err
		}
		if conflicted {
			conflict = true
			return errVersionConflict
		}
		newVersion = v

		prevJSON, _ := json.Marshal(prevValues)
		newJSON, _ := json.Marshal(newValues)
		pathsJSON, _ := json.Marshal(changedPaths)
		return profiledal.InsertProfileChangeEvent(tx, &profiledal.ProfileChangeEvent{
			AgentID:        agentID,
			SourceVersion:  newVersion,
			ActorType:      "agent",
			ActorID:        strconv.FormatInt(agentID, 10),
			Source:         truncate(req.Source, 100),
			Reason:         truncate(req.Reason, 2000),
			ChangedPaths:   string(pathsJSON),
			PreviousValues: string(prevJSON),
			NewValues:      string(newJSON),
			RequestID:      truncate(string(c.GetHeader("X-Request-ID")), 100),
		})
	})
	if err != nil {
		if conflict {
			respond(c, http.StatusConflict, 409, "profile changed after refresh context was fetched", map[string]interface{}{
				"error_code": "PROFILE_VERSION_CONFLICT",
				"action":     "refetch_refresh_context",
			})
			return
		}
		logger.Ctx(ctx).Error("PutProfileFields failed", "agentID", agentID, "err", err)
		respond(c, http.StatusInternalServerError, 500, "failed to update profile", nil)
		return
	}

	if bioChanged {
		// Same side effects the legacy PUT /agents/profile has on bio change:
		// keyword/embedding reprocessing + console activity trail.
		_ = profiledal.UpdateAgentProfileStatus(db.DB, agentID, 0)
		activity.PublishProfileUpdate(ctx, agentID)
	}
	if nameChanged || bioChanged {
		_, _ = mq.Publish(ctx, "stream:profile:update", map[string]interface{}{
			"agent_id": strconv.FormatInt(agentID, 10),
		})
	}
	if !noChanges {
		agentcard.PublishRebuild(ctx, agentID, "profile_fields_update")
	}

	respond(c, http.StatusOK, 0, "success", map[string]interface{}{
		"profile_version": newVersion,
		"changed_paths":   changedPaths,
	})
}

var errVersionConflict = errors.New("profile version conflict")

func jsonValuesEqual(a, b json.RawMessage) bool {
	var av interface{}
	var bv interface{}
	if json.Unmarshal(a, &av) != nil || json.Unmarshal(b, &bv) != nil {
		return false
	}
	return reflect.DeepEqual(av, bv)
}

func truncate(s string, max int) string {
	rs := []rune(s)
	if len(rs) <= max {
		return s
	}
	return string(rs[:max])
}
