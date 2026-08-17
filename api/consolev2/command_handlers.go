package consolev2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"gorm.io/gorm"
)

const (
	commandLeaseTTL          = 2 * time.Minute
	commandListPayloadBudget = 220 << 10
)

type createAgentCommandRequest struct {
	CommandType    string          `json:"command_type"`
	Payload        json.RawMessage `json:"payload"`
	AttentionID    *string         `json:"attention_id,omitempty"`
	IdempotencyKey string          `json:"idempotency_key"`
}

func validCommandType(value string) bool {
	switch value {
	case "human_instruction", "private_message", "broadcast_reply", "trade_update", "attention_action":
		return true
	default:
		return false
	}
}

func (s *Service) createAgentCommand(_ context.Context, c *app.RequestContext) {
	agentIDValue, _ := agentID(c)
	var req createAgentCommandRequest
	if err := decodeBody(c, &req); err != nil || !validCommandType(req.CommandType) || req.IdempotencyKey == "" || len(req.IdempotencyKey) > 128 {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "command_type, payload, and idempotency_key are invalid", nil)
		return
	}
	if len(req.Payload) == 0 {
		req.Payload = json.RawMessage(`{}`)
	}
	var payloadObject map[string]interface{}
	if len(req.Payload) > 64<<10 || json.Unmarshal(req.Payload, &payloadObject) != nil {
		fail(c, http.StatusBadRequest, "INVALID_PAYLOAD", "command payload must be an object no larger than 64KB", nil)
		return
	}
	var attentionID *int64
	if req.AttentionID != nil {
		parsed, err := strconv.ParseInt(*req.AttentionID, 10, 64)
		if err != nil || parsed <= 0 {
			fail(c, http.StatusBadRequest, "INVALID_ATTENTION_ID", "attention_id is invalid", nil)
			return
		}
		attentionID = &parsed
	}
	requestHash := hashString(req.CommandType + "\x00" + string(req.Payload) + "\x00" + fmt.Sprint(attentionID))
	now := time.Now().UnixMilli()
	var commandID, contextRevision int64
	created := false
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var prior struct {
			CommandID   int64  `gorm:"column:command_id"`
			PayloadHash string `gorm:"column:payload_hash"`
		}
		if err := tx.Raw(`SELECT command_id, payload_hash FROM agent_commands
			WHERE agent_id = ? AND idempotency_key = ?`, agentIDValue, req.IdempotencyKey).Scan(&prior).Error; err != nil {
			return err
		}
		if prior.CommandID != 0 {
			if prior.PayloadHash != requestHash {
				return errConflict
			}
			commandID = prior.CommandID
			return nil
		}
		if err := tx.Raw(`SELECT active_revision FROM agent_context_heads
			WHERE agent_id = ? FOR UPDATE`, agentIDValue).Scan(&contextRevision).Error; err != nil {
			return err
		}
		if contextRevision <= 0 {
			return errConflict
		}
		if err := tx.Raw(`INSERT INTO agent_commands
			(agent_id, attention_id, command_type, payload, payload_hash, required_context_revision,
			 status, idempotency_key, created_at)
			VALUES (?, ?, ?, ?::jsonb, ?, ?, 'pending', ?, ?) RETURNING command_id`,
			agentIDValue, attentionID, req.CommandType, string(req.Payload), requestHash,
			contextRevision, req.IdempotencyKey, now).Scan(&commandID).Error; err != nil {
			if isUniqueViolation(err) {
				return errConflict
			}
			return err
		}
		if err := tx.Exec(`INSERT INTO control_wakeup_outbox
			(agent_id, event_type, entity_id, payload, status, next_attempt_at, created_at)
			VALUES (?, 'command_available', ?, jsonb_build_object('command_id', CAST(? AS text)), 'pending', ?, ?)
			ON CONFLICT (event_type, entity_id) DO NOTHING`, agentIDValue, commandID,
			fmt.Sprintf("%d", commandID), now, now).Error; err != nil {
			return err
		}
		created = true
		return nil
	})
	if errors.Is(err, errConflict) || isUniqueViolation(err) {
		var existing struct {
			CommandID       int64  `gorm:"column:command_id"`
			PayloadHash     string `gorm:"column:payload_hash"`
			ContextRevision int64  `gorm:"column:required_context_revision"`
		}
		readErr := s.db.Raw(`SELECT command_id, payload_hash, required_context_revision
			FROM agent_commands WHERE agent_id = ? AND idempotency_key = ?`,
			agentIDValue, req.IdempotencyKey).Scan(&existing).Error
		switch {
		case readErr != nil:
			err = readErr
		case existing.CommandID != 0 && existing.PayloadHash == requestHash:
			commandID, contextRevision, created, err = existing.CommandID, existing.ContextRevision, false, nil
		default:
			err = errConflict
		}
	}
	if errors.Is(err, errConflict) {
		fail(c, http.StatusConflict, "COMMAND_CONFLICT", "command idempotency key conflicts or no active context exists", nil)
		return
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, "COMMAND_CREATE_FAILED", "could not create Agent command", nil)
		return
	}
	reply(c, map[bool]int{true: http.StatusCreated, false: http.StatusOK}[created], map[string]interface{}{
		"command_id": fmt.Sprintf("%d", commandID), "created": created, "required_context_revision": contextRevision,
	})
}

type commandView struct {
	CommandID               int64   `gorm:"column:command_id"`
	CommandType             string  `gorm:"column:command_type"`
	Payload                 string  `gorm:"column:payload"`
	RequiredContextRevision *int64  `gorm:"column:required_context_revision"`
	Status                  string  `gorm:"column:status"`
	ClaimOwnerRuntimeID     *string `gorm:"column:claim_owner_runtime_id"`
	ClaimEpoch              int64   `gorm:"column:claim_epoch"`
	ClaimTokenHash          string  `gorm:"column:claim_token_hash"`
	ClaimUntil              *int64  `gorm:"column:claim_until"`
	AttemptCount            int     `gorm:"column:attempt_count"`
	CreatedAt               int64   `gorm:"column:created_at"`
}

func (s *Service) commandClaimToken(agentIDValue, commandID int64, runtimeInstanceID string, claimEpoch int64) string {
	seed := fmt.Sprintf("command-claim-v1\x00%d\x00%d\x00%s\x00%d",
		agentIDValue, commandID, hashString(runtimeInstanceID), claimEpoch)
	return "efclaim_" + keyedHash(s.otpPepper, seed)
}

func commandResponse(row commandView) map[string]interface{} {
	var payload map[string]interface{}
	_ = json.Unmarshal([]byte(row.Payload), &payload)
	return map[string]interface{}{
		"command_id": fmt.Sprintf("%d", row.CommandID), "command_type": row.CommandType,
		"payload": payload, "required_context_revision": row.RequiredContextRevision,
		"status": row.Status, "claim_owner_runtime_id": row.ClaimOwnerRuntimeID,
		"claim_epoch": row.ClaimEpoch, "claim_until": row.ClaimUntil,
		"attempt_count": row.AttemptCount, "created_at": row.CreatedAt,
	}
}

func (s *Service) listPendingCommands(_ context.Context, c *app.RequestContext) {
	agentIDValue, _ := agentID(c)
	limit := 20
	if raw := c.Query("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 50 {
			fail(c, http.StatusBadRequest, "INVALID_LIMIT", "limit must be between 1 and 50", nil)
			return
		}
		limit = parsed
	}
	now := time.Now().UnixMilli()
	var rows []commandView
	if err := s.db.Raw(`WITH base AS (
			SELECT command_id, command_type, payload, required_context_revision, status,
				claim_owner_runtime_id, claim_epoch, claim_until, attempt_count, created_at
			FROM agent_commands
			WHERE agent_id = ? AND (status IN ('pending','notified') OR (status = 'claimed' AND claim_until <= ?))
			ORDER BY created_at, command_id LIMIT ?
		), candidates AS (
			SELECT base.*,
				row_number() OVER (ORDER BY created_at, command_id) AS row_num,
				sum(octet_length(payload::text) + 256) OVER (ORDER BY created_at, command_id) AS cumulative_bytes
			FROM base
		) SELECT command_id, command_type, payload::text AS payload, required_context_revision,
			status, claim_owner_runtime_id, claim_epoch, claim_until, attempt_count, created_at
		FROM candidates WHERE cumulative_bytes <= ? OR row_num = 1
		ORDER BY created_at, command_id`, agentIDValue, now, limit, commandListPayloadBudget).Scan(&rows).Error; err != nil {
		fail(c, http.StatusInternalServerError, "COMMAND_LIST_FAILED", "could not list Agent commands", nil)
		return
	}
	commands := make([]map[string]interface{}, 0, len(rows))
	for _, row := range rows {
		commands = append(commands, commandResponse(row))
	}
	reply(c, http.StatusOK, map[string]interface{}{"commands": commands})
}

type claimAgentCommandRequest struct {
	RuntimeInstanceID      string `json:"runtime_instance_id"`
	AppliedContextRevision int64  `json:"applied_context_revision"`
}

func (s *Service) claimAgentCommand(_ context.Context, c *app.RequestContext) {
	agentIDValue, _ := agentID(c)
	commandID, err := strconv.ParseInt(c.Param("command_id"), 10, 64)
	var req claimAgentCommandRequest
	if err != nil || decodeBody(c, &req) != nil || req.RuntimeInstanceID == "" || len(req.RuntimeInstanceID) > 128 || req.AppliedContextRevision < 0 {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "command_id, runtime_instance_id, and applied_context_revision are required", nil)
		return
	}
	var claimToken string
	var now, claimUntil int64
	var row commandView
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT command_id, command_type, payload::text AS payload,
			required_context_revision, status, claim_owner_runtime_id, claim_epoch, claim_token_hash, claim_until,
			attempt_count, created_at FROM agent_commands
			WHERE command_id = ? AND agent_id = ? FOR UPDATE`, commandID, agentIDValue).Scan(&row).Error; err != nil {
			return err
		}
		var runtime struct {
			AppliedRevision *int64 `gorm:"column:context_revision_applied"`
			Now             int64  `gorm:"column:now_ms"`
		}
		if err := tx.Raw(`WITH clock AS (
				SELECT (extract(epoch FROM clock_timestamp())*1000)::bigint AS now_ms
			) SELECT lease.context_revision_applied, clock.now_ms
			FROM agent_runtime_leases lease CROSS JOIN clock
			WHERE lease.agent_id = ? AND lease.runtime_instance_id = ?
			  AND lease.lease_until > clock.now_ms
			FOR UPDATE OF lease`, agentIDValue, req.RuntimeInstanceID).Scan(&runtime).Error; err != nil {
			return err
		}
		if runtime.Now == 0 || runtime.AppliedRevision == nil || req.AppliedContextRevision != *runtime.AppliedRevision ||
			(row.RequiredContextRevision != nil && *runtime.AppliedRevision < *row.RequiredContextRevision) {
			return errOnboardingRequired
		}
		now = runtime.Now
		if row.CommandID != 0 && row.Status == "claimed" && row.ClaimOwnerRuntimeID != nil &&
			*row.ClaimOwnerRuntimeID == req.RuntimeInstanceID && row.ClaimUntil != nil && *row.ClaimUntil > now {
			claimToken = s.commandClaimToken(agentIDValue, commandID, req.RuntimeInstanceID, row.ClaimEpoch)
			if row.ClaimTokenHash != hashString(claimToken) {
				return errConflict
			}
			return nil
		}
		claimUntil = now + int64(commandLeaseTTL/time.Millisecond)
		if row.CommandID == 0 || (row.Status != "pending" && row.Status != "notified" &&
			!(row.Status == "claimed" && row.ClaimUntil != nil && *row.ClaimUntil <= now)) {
			return errConflict
		}
		row.ClaimEpoch++
		claimToken = s.commandClaimToken(agentIDValue, commandID, req.RuntimeInstanceID, row.ClaimEpoch)
		row.Status = "claimed"
		row.ClaimOwnerRuntimeID = &req.RuntimeInstanceID
		row.ClaimUntil = &claimUntil
		row.AttemptCount++
		return tx.Exec(`UPDATE agent_commands SET status = 'claimed', claim_owner_runtime_id = ?,
			claim_epoch = ?, claim_token_hash = ?, claim_until = ?, attempt_count = attempt_count + 1,
			delivered_at = COALESCE(delivered_at, ?)
			WHERE command_id = ?`, req.RuntimeInstanceID, row.ClaimEpoch, hashString(claimToken),
			claimUntil, now, commandID).Error
	})
	if errors.Is(err, errOnboardingRequired) {
		fail(c, http.StatusConflict, "CONTEXT_REQUIRED", "apply the required control context before claiming this command", map[string]interface{}{
			"required_context_revision": row.RequiredContextRevision,
		})
		return
	}
	if errors.Is(err, errConflict) {
		fail(c, http.StatusConflict, "COMMAND_CLAIMED", "command is unavailable or already claimed", nil)
		return
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, "COMMAND_CLAIM_FAILED", "could not claim Agent command", nil)
		return
	}
	data := commandResponse(row)
	data["claim_token"] = claimToken
	reply(c, http.StatusOK, data)
}

type completeAgentCommandRequest struct {
	RuntimeInstanceID string          `json:"runtime_instance_id"`
	ClaimEpoch        int64           `json:"claim_epoch"`
	ClaimToken        string          `json:"claim_token"`
	Status            string          `json:"status"`
	Result            json.RawMessage `json:"result"`
}

func (s *Service) completeAgentCommand(_ context.Context, c *app.RequestContext) {
	agentIDValue, _ := agentID(c)
	commandID, err := strconv.ParseInt(c.Param("command_id"), 10, 64)
	var req completeAgentCommandRequest
	if err != nil || decodeBody(c, &req) != nil || req.RuntimeInstanceID == "" || req.ClaimEpoch <= 0 || req.ClaimToken == "" ||
		(req.Status != "completed" && req.Status != "failed") {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "command completion proof and status are invalid", nil)
		return
	}
	if len(req.Result) == 0 {
		req.Result = json.RawMessage(`{}`)
	}
	var resultObject map[string]interface{}
	if len(req.Result) > 64<<10 || json.Unmarshal(req.Result, &resultObject) != nil {
		fail(c, http.StatusBadRequest, "INVALID_RESULT", "command result must be an object no larger than 64KB", nil)
		return
	}
	now := time.Now().UnixMilli()
	completedAt := now
	res := s.db.Exec(`WITH clock AS (
			SELECT (extract(epoch FROM clock_timestamp())*1000)::bigint AS now_ms
		) UPDATE agent_commands SET status = ?, result = ?::jsonb, completed_at = ?
		FROM clock
		WHERE command_id = ? AND agent_id = ? AND status = 'claimed'
		  AND claim_owner_runtime_id = ? AND claim_epoch = ? AND claim_token_hash = ?
		  AND claim_until > clock.now_ms`,
		req.Status, string(req.Result), now, commandID, agentIDValue, req.RuntimeInstanceID,
		req.ClaimEpoch, hashString(req.ClaimToken))
	if res.Error != nil {
		fail(c, http.StatusInternalServerError, "COMMAND_COMPLETE_FAILED", "could not complete Agent command", nil)
		return
	}
	if res.RowsAffected != 1 {
		var existing struct {
			Status         string `gorm:"column:status"`
			SameResult     bool   `gorm:"column:same_result"`
			ClaimOwner     string `gorm:"column:claim_owner_runtime_id"`
			ClaimEpoch     int64  `gorm:"column:claim_epoch"`
			ClaimTokenHash string `gorm:"column:claim_token_hash"`
			CompletedAt    int64  `gorm:"column:completed_at"`
		}
		if err := s.db.Raw(`SELECT status, result = ?::jsonb AS same_result, claim_owner_runtime_id,
			claim_epoch, claim_token_hash, completed_at FROM agent_commands
			WHERE command_id = ? AND agent_id = ?`, string(req.Result), commandID, agentIDValue).Scan(&existing).Error; err != nil {
			fail(c, http.StatusInternalServerError, "COMMAND_COMPLETE_FAILED", "could not verify Agent command completion", nil)
			return
		}
		if existing.Status != req.Status || !existing.SameResult || existing.ClaimOwner != req.RuntimeInstanceID ||
			existing.ClaimEpoch != req.ClaimEpoch || existing.ClaimTokenHash != hashString(req.ClaimToken) {
			fail(c, http.StatusConflict, "CLAIM_FENCED", "command claim is stale or owned by another runtime", nil)
			return
		}
		completedAt = existing.CompletedAt
	}
	reply(c, http.StatusOK, map[string]interface{}{"command_id": fmt.Sprintf("%d", commandID), "status": req.Status, "completed_at": completedAt})
}
