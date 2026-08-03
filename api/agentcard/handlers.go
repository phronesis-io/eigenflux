// Package agentcardapi serves the Agent Card read/write endpoints. Routes are
// hand-registered (like api/install and the settings routes) so no IDL/router
// regen is needed, and every response is a whitelist DTO assembled here —
// never a raw agent_cards row.
package agentcardapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"eigenflux_server/api/middleware"
	"eigenflux_server/pkg/activity"
	"eigenflux_server/pkg/agentcard"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/mq"
	pmdal "eigenflux_server/rpc/pm/dal"
	profiledal "eigenflux_server/rpc/profile/dal"
)

const (
	// maxFieldsPerUpdate bounds one PUT /agents/me/profile/fields request.
	maxFieldsPerUpdate = 32
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
	profileWriteDailyLimit   = 100
	profileWriteDailyWindow  = 24 * time.Hour
)

var fixedWindowCounterScript = redis.NewScript(`
local count = redis.call("INCR", KEYS[1])
if count == 1 then
  redis.call("EXPIRE", KEYS[1], ARGV[1])
end
return count
`)

// Register wires the Agent Card routes onto the gateway.
func Register(h *server.Hertz) {
	h.GET("/api/v1/agents/me/card", middleware.AuthMiddleware(), GetMyCard)
	h.GET("/api/v1/agents/me/card/refresh-context", middleware.AuthMiddleware(), GetRefreshContext)
	h.PUT("/api/v1/agents/me/profile/fields", middleware.AuthMiddleware(), PutProfileFields)
	h.GET("/api/v1/agents/:agent_id/card", middleware.AuthMiddleware(), GetPublicCard)
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
		return card, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if rerr := agentcard.Rebuild(ctx, db.DB, mq.RDB, agentID); rerr != nil {
		return nil, rerr
	}
	return profiledal.GetAgentCard(db.DB, agentID)
}

// GetMyCard returns the caller's full card (public + private projections).
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
	respond(c, http.StatusOK, 0, "success", map[string]interface{}{
		"public":       json.RawMessage(card.PublicCard),
		"private":      json.RawMessage(card.PrivateCard),
		"card_version": card.CardVersion,
		"generated_at": card.GeneratedAt,
	})
}

// GetPublicCard returns another agent's public card plus the viewer-relative
// relation block. Blocked pairs (either direction) get an indistinguishable
// 404 so blocking is not observable.
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

	if !allowPublicCardRead(ctx, viewerID) {
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

	respond(c, http.StatusOK, 0, "success", map[string]interface{}{
		"card":               json.RawMessage(card.PublicCard),
		"relation_to_viewer": relationToViewer(viewerID, targetID),
	})
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

// allowFixedWindow applies a per-agent fixed-window counter. Redis failures
// fail open so a limiter outage cannot take profile/card APIs down, but the
// failure is logged with the scope so it is operationally visible.
func allowFixedWindow(ctx context.Context, rdb *redis.Client, agentID int64, scope string, limit int64, window time.Duration, now time.Time) bool {
	if rdb == nil {
		logger.Ctx(ctx).Warn("agent card rate limiter unavailable, failing open", "agentID", agentID, "scope", scope, "err", "redis client is nil")
		return true
	}
	windowSeconds := int64(window.Seconds())
	if limit <= 0 || windowSeconds <= 0 {
		logger.Ctx(ctx).Warn("invalid agent card rate limit, failing open", "agentID", agentID, "scope", scope, "limit", limit, "window", window)
		return true
	}
	key := fmt.Sprintf("agentcard:rl:%s:%d:%d", scope, agentID, now.Unix()/windowSeconds)
	n, err := fixedWindowCounterScript.Run(ctx, rdb, []string{key}, windowSeconds+1).Int64()
	if err != nil {
		logger.Ctx(ctx).Warn("agent card rate limiter unavailable, failing open", "agentID", agentID, "scope", scope, "err", err)
		return true
	}
	return n <= limit
}

// GetRefreshContext gives the agent everything it needs before an automated
// refresh: current version, per-field current/previous value + last actor,
// and the protected paths it must not write.
func GetRefreshContext(ctx context.Context, c *app.RequestContext) {
	agentID, ok := callerAgentID(c)
	if !ok {
		return
	}
	if !allowFixedWindow(ctx, mq.RDB, agentID, "refresh-context", refreshContextRateLimit, refreshContextRateWindow, time.Now()) {
		respond(c, http.StatusTooManyRequests, 429, "too many profile refresh reads, slow down", nil)
		return
	}
	agent, err := profiledal.GetAgentByID(db.DB, agentID)
	if err != nil {
		respond(c, http.StatusNotFound, 404, "agent not found", nil)
		return
	}
	version, profileData, err := profiledal.GetProfileVersionAndData(db.DB, agentID)
	if err != nil {
		logger.Ctx(ctx).Error("GetRefreshContext read failed", "agentID", agentID, "err", err)
		respond(c, http.StatusInternalServerError, 500, "failed to load profile", nil)
		return
	}

	type lastChange struct {
		prevValue json.RawMessage
		updatedAt int64
		actorType string
		reason    string
		source    string
	}
	lastByPath := map[string]lastChange{}
	if events, eerr := profiledal.ListRecentProfileChangeEvents(db.DB, agentID, 200); eerr == nil {
		for _, ev := range events { // newest first; first hit per path wins
			var paths []string
			if json.Unmarshal([]byte(ev.ChangedPaths), &paths) != nil {
				continue
			}
			var prevVals map[string]json.RawMessage
			_ = json.Unmarshal([]byte(ev.PreviousValues), &prevVals)
			for _, p := range paths {
				if _, seen := lastByPath[p]; seen {
					continue
				}
				lastByPath[p] = lastChange{
					prevValue: prevVals[p],
					updatedAt: ev.CreatedAt,
					actorType: ev.ActorType,
					reason:    ev.Reason,
					source:    ev.Source,
				}
			}
		}
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
			entry["last_reason"] = lc.reason
			entry["last_source"] = lc.source
		}
		editable[spec.Name] = entry
	}

	respond(c, http.StatusOK, 0, "success", map[string]interface{}{
		"profile_version": version,
		"editable_fields": editable,
		"protected_paths": agentcard.ProtectedPaths,
	})
}

type profileFieldsReq struct {
	ExpectedVersion *int64                     `json:"expected_version"`
	Updates         map[string]json.RawMessage `json:"updates"`
	Source          string                     `json:"source"`
	Reason          string                     `json:"reason"`
}

// PutProfileFields is the field-level versioned profile write. All writes go
// to the fact tables (agents / agent_profiles.profile_data); the card is
// rebuilt asynchronously. actor_type is derived from the credential (agent
// token ⇒ "agent"), never trusted from the request body.
func PutProfileFields(ctx context.Context, c *app.RequestContext) {
	agentID, ok := callerAgentID(c)
	if !ok {
		return
	}
	if !allowFixedWindow(ctx, mq.RDB, agentID, "profile-write-minute", profileWriteMinuteLimit, profileWriteMinuteWindow, time.Now()) ||
		!allowFixedWindow(ctx, mq.RDB, agentID, "profile-write-day", profileWriteDailyLimit, profileWriteDailyWindow, time.Now()) {
		respond(c, http.StatusTooManyRequests, 429, "too many profile updates, slow down", nil)
		return
	}
	body, berr := c.Body()
	if berr != nil {
		respond(c, http.StatusBadRequest, 400, "failed to read request body", nil)
		return
	}
	var req profileFieldsReq
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

	var newVersion int64
	var conflict bool
	var bioChanged bool
	err := db.DB.Transaction(func(tx *gorm.DB) error {
		if err := profiledal.EnsureAgentProfileRow(tx, agentID); err != nil {
			return err
		}
		agent, err := profiledal.GetAgentByID(tx, agentID)
		if err != nil {
			return err
		}
		_, prevData, err := profiledal.GetProfileVersionAndData(tx, agentID)
		if err != nil {
			return err
		}

		// Previous values for the audit event, from the same snapshot the
		// conditional update is about to replace.
		prevValues := map[string]json.RawMessage{}
		for _, p := range changedPaths {
			switch p {
			case "agent_name":
				b, _ := json.Marshal(agent.AgentName)
				prevValues[p] = b
			case "agent_description":
				b, _ := json.Marshal(agent.Bio)
				prevValues[p] = b
			default:
				if raw, ok := prevData[p]; ok {
					prevValues[p] = raw
				}
			}
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
		_, _ = mq.Publish(ctx, "stream:profile:update", map[string]interface{}{
			"agent_id": strconv.FormatInt(agentID, 10),
		})
		activity.PublishProfileUpdate(ctx, agentID)
	}
	agentcard.PublishRebuild(ctx, agentID, "profile_fields_update")

	respond(c, http.StatusOK, 0, "success", map[string]interface{}{
		"profile_version": newVersion,
		"changed_paths":   changedPaths,
	})
}

var errVersionConflict = errors.New("profile version conflict")

func truncate(s string, max int) string {
	rs := []rune(s)
	if len(rs) <= max {
		return s
	}
	return string(rs[:max])
}
