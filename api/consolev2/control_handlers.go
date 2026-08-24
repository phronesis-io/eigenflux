package consolev2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cloudwego/hertz/pkg/app"
	"gorm.io/gorm"

	agentcardapi "eigenflux_server/api/agentcard"
	"eigenflux_server/pkg/agentcard"
	"eigenflux_server/pkg/logger"
	profiledal "eigenflux_server/rpc/profile/dal"
)

var errIntentLimitReached = errors.New("active intent limit reached")

type ContextWriteRequest struct {
	ExpectedContextRevision int64  `json:"expected_context_revision"`
	IdempotencyKey          string `json:"idempotency_key"`
}

func validateContextWrite(base ContextWriteRequest) error {
	if base.ExpectedContextRevision <= 0 || base.IdempotencyKey == "" || len(base.IdempotencyKey) > 128 {
		return errors.New("expected_context_revision and idempotency_key are required")
	}
	return nil
}

func lockContextHead(tx *gorm.DB, agentID, expected int64) error {
	var head struct {
		ActiveRevision *int64 `gorm:"column:active_revision"`
	}
	if err := tx.Raw(`SELECT active_revision FROM agent_context_heads WHERE agent_id = ? FOR UPDATE`, agentID).Scan(&head).Error; err != nil {
		return err
	}
	if head.ActiveRevision == nil || *head.ActiveRevision != expected {
		return errConflict
	}
	return nil
}

func activateLatestContext(tx *gorm.DB, agentID, now int64) (int64, error) {
	_, revision, err := compileAndActivateContext(tx, agentID, now)
	if err != nil {
		return 0, err
	}
	if res := tx.Exec(`UPDATE agent_onboarding_v2 SET active_context_revision = ?, updated_at = ?
		WHERE agent_id = ? AND state = 'completed'`, revision, now, agentID); res.Error != nil || res.RowsAffected != 1 {
		return 0, errConflict
	}
	return revision, nil
}

type networkGoalWriteRequest struct {
	ContextWriteRequest
	GoalText string `json:"goal_text"`
}

func (s *Service) putNetworkGoal(_ context.Context, c *app.RequestContext) {
	id, _ := agentID(c)
	var req networkGoalWriteRequest
	if err := decodeBody(c, &req); err != nil || validateContextWrite(req.ContextWriteRequest) != nil || strings.TrimSpace(req.GoalText) == "" || utf8.RuneCountInString(req.GoalText) > 2000 {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "valid goal_text, expected_context_revision, and idempotency_key are required", nil)
		return
	}
	requestHash := hashString(fmt.Sprintf("%d:%s", req.ExpectedContextRevision, req.GoalText))
	revision, replay, err := s.contextMutation(id, "network_goal_put", req.IdempotencyKey, requestHash, func(tx *gorm.DB, now int64) error {
		if err := lockContextHead(tx, id, req.ExpectedContextRevision); err != nil {
			return err
		}
		if err := tx.Exec(`UPDATE agent_network_goals SET status = 'deleted', updated_at = ?
			WHERE agent_id = ? AND status = 'active'`, now, id).Error; err != nil {
			return err
		}
		return tx.Exec(`INSERT INTO agent_network_goals
			(agent_id, goal_text, source, status, version, created_at, updated_at)
			VALUES (?, ?, 'human_edit', 'active', 1, ?, ?)`, id, req.GoalText, now, now).Error
	})
	respondContextMutation(c, revision, replay, err)
}

type IntentWriteFields struct {
	WatchFor          string `json:"watch_for"`
	TriggerWhen       string `json:"trigger_when"`
	ActionInstruction string `json:"action_instruction"`
	ActionPolicy      string `json:"action_policy"`
	Priority          int16  `json:"priority"`
}

func validateIntent(fields IntentWriteFields) error {
	if strings.TrimSpace(fields.WatchFor) == "" {
		return errors.New("watch_for is required")
	}
	if utf8.RuneCountInString(fields.WatchFor) > 1000 || utf8.RuneCountInString(fields.TriggerWhen) > 1000 || utf8.RuneCountInString(fields.ActionInstruction) > 2000 {
		return errors.New("intent text exceeds its length limit")
	}
	switch fields.ActionPolicy {
	case "analyze_only", "draft", "network_action", "trade_action":
		return nil
	default:
		return errors.New("unsupported action_policy")
	}
}

type createIntentRequest struct {
	ContextWriteRequest
	IntentWriteFields
}

func (s *Service) createIntentAction(_ context.Context, c *app.RequestContext) {
	id, _ := agentID(c)
	var req createIntentRequest
	if err := decodeBody(c, &req); err != nil || validateContextWrite(req.ContextWriteRequest) != nil || validateIntent(req.IntentWriteFields) != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid intent action request", nil)
		return
	}
	encoded, _ := json.Marshal(req.IntentWriteFields)
	requestHash := hashString(fmt.Sprintf("%d:%s", req.ExpectedContextRevision, encoded))
	revision, replay, err := s.contextMutation(id, "intent_create", req.IdempotencyKey, requestHash, func(tx *gorm.DB, now int64) error {
		if err := lockContextHead(tx, id, req.ExpectedContextRevision); err != nil {
			return err
		}
		var count int64
		if err := tx.Raw(`SELECT COUNT(*) FROM agent_intent_actions WHERE agent_id = ? AND status = 'active'`, id).Scan(&count).Error; err != nil {
			return err
		}
		if count >= 10 {
			return errIntentLimitReached
		}
		return tx.Exec(`INSERT INTO agent_intent_actions
			(agent_id, watch_for, trigger_when, action_instruction, action_policy, priority,
			 source, status, version, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, 'human_edit', 'active', 1, ?, ?)`, id,
			req.WatchFor, req.TriggerWhen, req.ActionInstruction, req.ActionPolicy, req.Priority, now, now).Error
	})
	respondContextMutation(c, revision, replay, err)
}

type updateIntentRequest struct {
	ContextWriteRequest
	IntentWriteFields
}

func parseIntentID(c *app.RequestContext) (int64, error) {
	id, err := strconv.ParseInt(c.Param("intent_id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("invalid intent_id")
	}
	return id, nil
}

func (s *Service) updateIntentAction(_ context.Context, c *app.RequestContext) {
	agentIDValue, _ := agentID(c)
	intentID, idErr := parseIntentID(c)
	var req updateIntentRequest
	if idErr != nil || decodeBody(c, &req) != nil || validateContextWrite(req.ContextWriteRequest) != nil || validateIntent(req.IntentWriteFields) != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid intent action request", nil)
		return
	}
	encoded, _ := json.Marshal(req.IntentWriteFields)
	requestHash := hashString(fmt.Sprintf("%d:%d:%s", req.ExpectedContextRevision, intentID, encoded))
	revision, replay, err := s.contextMutation(agentIDValue, "intent_update", req.IdempotencyKey, requestHash, func(tx *gorm.DB, now int64) error {
		if err := lockContextHead(tx, agentIDValue, req.ExpectedContextRevision); err != nil {
			return err
		}
		res := tx.Exec(`UPDATE agent_intent_actions SET watch_for = ?, trigger_when = ?,
			action_instruction = ?, action_policy = ?, priority = ?, source = 'human_edit',
			version = version + 1, updated_at = ?
			WHERE agent_id = ? AND intent_id = ? AND status <> 'deleted'`, req.WatchFor,
			req.TriggerWhen, req.ActionInstruction, req.ActionPolicy, req.Priority, now, agentIDValue, intentID)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
	respondContextMutation(c, revision, replay, err)
}

type deleteIntentRequest struct{ ContextWriteRequest }

func (s *Service) deleteIntentAction(_ context.Context, c *app.RequestContext) {
	agentIDValue, _ := agentID(c)
	intentID, idErr := parseIntentID(c)
	var req deleteIntentRequest
	if idErr != nil || decodeBody(c, &req) != nil || validateContextWrite(req.ContextWriteRequest) != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "invalid intent deletion request", nil)
		return
	}
	requestHash := hashString(fmt.Sprintf("%d:%d", req.ExpectedContextRevision, intentID))
	revision, replay, err := s.contextMutation(agentIDValue, "intent_delete", req.IdempotencyKey, requestHash, func(tx *gorm.DB, now int64) error {
		if err := lockContextHead(tx, agentIDValue, req.ExpectedContextRevision); err != nil {
			return err
		}
		res := tx.Exec(`UPDATE agent_intent_actions SET status = 'deleted', version = version + 1, updated_at = ?
			WHERE agent_id = ? AND intent_id = ? AND status <> 'deleted'`, now, agentIDValue, intentID)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
	respondContextMutation(c, revision, replay, err)
}

func (s *Service) contextMutation(agentID int64, operation, key, requestHash string, mutate func(*gorm.DB, int64) error) (revision int64, replay bool, err error) {
	now := time.Now().UnixMilli()
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var prior struct {
			RequestHash string `gorm:"column:request_hash"`
			Response    string `gorm:"column:response_snapshot"`
		}
		if err := tx.Raw(`SELECT request_hash, response_snapshot::text AS response_snapshot
			FROM agent_idempotency_requests WHERE agent_id = ? AND operation = ? AND idempotency_key = ?`, agentID, operation, key).Scan(&prior).Error; err != nil {
			return err
		}
		if prior.RequestHash != "" {
			if prior.RequestHash != requestHash {
				return errConflict
			}
			var snapshot struct {
				ContextRevision int64 `json:"context_revision"`
			}
			if err := json.Unmarshal([]byte(prior.Response), &snapshot); err != nil {
				return err
			}
			revision, replay = snapshot.ContextRevision, true
			return nil
		}
		if err := mutate(tx, now); err != nil {
			return err
		}
		revision, err = activateLatestContext(tx, agentID, now)
		if err != nil {
			return err
		}
		snapshot, _ := json.Marshal(map[string]interface{}{"context_revision": revision})
		return tx.Exec(`INSERT INTO agent_idempotency_requests
			(agent_id, operation, idempotency_key, request_hash, response_snapshot, expires_at, created_at)
			VALUES (?, ?, ?, ?, ?::jsonb, ?, ?)`, agentID, operation, key, requestHash, string(snapshot),
			now+int64(24*time.Hour/time.Millisecond), now).Error
	})
	if err != nil && (errors.Is(err, errConflict) || isUniqueViolation(err)) {
		var snapshot struct {
			ContextRevision int64 `json:"context_revision"`
		}
		found, hashConflict, replayErr := s.loadIdempotentResponse(agentID, operation, key, requestHash, &snapshot)
		switch {
		case replayErr != nil:
			err = replayErr
		case found && hashConflict:
			err = errConflict
		case found:
			revision, replay, err = snapshot.ContextRevision, true, nil
		}
	}
	return
}

func respondContextMutation(c *app.RequestContext, revision int64, replay bool, err error) {
	switch {
	case errors.Is(err, errConflict):
		fail(c, http.StatusConflict, "REVISION_CONFLICT", "context changed or idempotency key was reused", nil)
	case errors.Is(err, gorm.ErrRecordNotFound):
		fail(c, http.StatusNotFound, "INTENT_NOT_FOUND", "intent action was not found", nil)
	case errors.Is(err, errIntentLimitReached):
		fail(c, http.StatusBadRequest, "INTENT_LIMIT_REACHED", "at most 10 active intent actions are allowed", nil)
	case err != nil:
		logger.Default().Error("Console V2 context update failed", "err", err)
		fail(c, http.StatusInternalServerError, "CONTEXT_UPDATE_FAILED", "could not update Agent context", nil)
	default:
		reply(c, http.StatusOK, map[string]interface{}{"context_revision": revision, "idempotent_replay": replay})
	}
}

type securityBoundaryRequest struct {
	ContextWriteRequest
	RecurringPublish bool `json:"recurring_publish"`
	AutoReplyPM      bool `json:"auto_reply_pm"`
	AutoComment      bool `json:"auto_comment"`
	ShowAddFriend    bool `json:"show_add_friend"`
}

func (s *Service) putSecurityBoundary(_ context.Context, c *app.RequestContext) {
	id, _ := agentID(c)
	var req securityBoundaryRequest
	if err := decodeBody(c, &req); err != nil || validateContextWrite(req.ContextWriteRequest) != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "expected_context_revision and idempotency_key are required", nil)
		return
	}
	encoded, _ := json.Marshal(map[string]bool{
		"recurring_publish": req.RecurringPublish, "auto_reply_pm": req.AutoReplyPM,
		"auto_comment": req.AutoComment, "show_add_friend": req.ShowAddFriend,
	})
	requestHash := hashString(fmt.Sprintf("%d:%s", req.ExpectedContextRevision, encoded))
	revision, replay, err := s.contextMutation(id, "security_boundary_put", req.IdempotencyKey, requestHash, func(tx *gorm.DB, now int64) error {
		if err := lockContextHead(tx, id, req.ExpectedContextRevision); err != nil {
			return err
		}
		return tx.Exec(`INSERT INTO agent_settings
			(agent_id, recurring_publish, auto_reply_pm, auto_comment, show_add_friend, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (agent_id) DO UPDATE SET recurring_publish = EXCLUDED.recurring_publish,
				auto_reply_pm = EXCLUDED.auto_reply_pm, auto_comment = EXCLUDED.auto_comment,
				show_add_friend = EXCLUDED.show_add_friend, updated_at = EXCLUDED.updated_at`, id,
			req.RecurringPublish, req.AutoReplyPM, req.AutoComment, req.ShowAddFriend, now).Error
	})
	respondContextMutation(c, revision, replay, err)
}

type profileFieldsRequest struct {
	AgentName string `json:"agent_name"`
	Bio       string `json:"bio"`
}

func validateIdentityCardFields(name, bio string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("valid agent_name and bio are required")
	}
	for field, value := range map[string]string{"agent_name": name, "agent_description": bio} {
		spec, ok := agentcard.LookupField(field)
		if !ok {
			return errors.New("identity field registry is unavailable")
		}
		raw, _ := json.Marshal(value)
		normalized, err := agentcard.ValidateValue(spec, raw)
		if err != nil {
			return err
		}
		if err := agentcard.ValidatePublicContent(spec, normalized); err != nil {
			return err
		}
		if err := agentcard.ValidateConsoleV2Value(field, normalized); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) putProfileFields(ctx context.Context, c *app.RequestContext) {
	id, _ := agentID(c)
	var req profileFieldsRequest
	if err := decodeBody(c, &req); err != nil || validateIdentityCardFields(req.AgentName, req.Bio) != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "valid agent_name and bio are required", nil)
		return
	}
	allowed, rateErr := agentcardapi.CheckProfileWriteRate(ctx, id)
	if rateErr != nil {
		fail(c, http.StatusServiceUnavailable, "PROFILE_RATE_LIMIT_UNAVAILABLE", "profile write protection is temporarily unavailable", nil)
		return
	}
	if !allowed {
		c.Header("Retry-After", "60")
		fail(c, http.StatusTooManyRequests, "PROFILE_RATE_LIMITED", "too many profile updates", nil)
		return
	}
	now := time.Now().UnixMilli()
	changed := false
	err := s.db.Transaction(func(tx *gorm.DB) error {
		agent, err := profiledal.GetAgentByIDForUpdate(tx, id)
		if err != nil {
			return err
		}
		nameChanged := agent.AgentName != req.AgentName
		bioChanged := agent.Bio != req.Bio
		if !nameChanged && !bioChanged {
			return nil
		}
		updates := map[string]interface{}{"agent_name": req.AgentName, "bio": req.Bio}
		if nameChanged {
			updates["agent_name_en"] = ""
		}
		if err := profiledal.UpdateAgentFields(tx, id, updates); err != nil {
			return err
		}
		if err := profiledal.EnsureAgentProfileRow(tx, id); err != nil {
			return err
		}
		newVersion, err := profiledal.BumpProfileVersion(tx, id)
		if err != nil {
			return err
		}
		if bioChanged {
			if err := profiledal.InsertBioHistory(tx, id, agent.Bio, req.Bio, "console_v2", "human_edit"); err != nil {
				return err
			}
		}
		paths := make([]string, 0, 2)
		previous := map[string]string{}
		next := map[string]string{}
		if nameChanged {
			paths = append(paths, "agent_name")
			previous["agent_name"], next["agent_name"] = agent.AgentName, req.AgentName
		}
		if bioChanged {
			paths = append(paths, "agent_description")
			previous["agent_description"], next["agent_description"] = agent.Bio, req.Bio
		}
		pathsJSON, _ := json.Marshal(paths)
		previousJSON, _ := json.Marshal(previous)
		nextJSON, _ := json.Marshal(next)
		if err := profiledal.InsertProfileChangeEvent(tx, &profiledal.ProfileChangeEvent{
			AgentID: id, SourceVersion: newVersion, ActorType: "human", ActorID: fmt.Sprintf("%d", id),
			Source: "console_v2", Reason: "human_edit", ChangedPaths: string(pathsJSON),
			PreviousValues: string(previousJSON), NewValues: string(nextJSON),
		}); err != nil {
			return err
		}
		changed = true
		return nil
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "PROFILE_SAVE_FAILED", "could not save Agent profile", nil)
		return
	}
	if changed {
		agentcard.PublishRebuild(ctx, id, "console_v2_profile_update")
	}
	reply(c, http.StatusOK, map[string]interface{}{"updated_at": now})
}
