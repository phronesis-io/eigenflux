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

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/lib/pq"
	"gorm.io/gorm"

	feedrpc "eigenflux_server/kitex_gen/eigenflux/feed"
)

const (
	feedLeaseTTL        = 2 * time.Minute
	feedMaxLeaseAge     = 10 * time.Minute
	feedBuildStaleAfter = 30 * time.Second
	feedMaxItems        = 20
)

var (
	errFeedBuilding  = errors.New("feed batch is building")
	errFeedLeaseHeld = errors.New("feed batch lease is held")
)

type createFeedBatchRequest struct {
	ProcessingScope   string           `json:"processing_scope"`
	RuntimeInstanceID string           `json:"runtime_instance_id"`
	IdempotencyKey    string           `json:"idempotency_key"`
	Limit             int32            `json:"limit"`
	KnownCardVersions map[string]int64 `json:"known_public_card_versions,omitempty"`
}

type feedBatchRow struct {
	BatchID                   int64   `gorm:"column:batch_id"`
	IdempotencyKey            string  `gorm:"column:idempotency_key"`
	RequestHash               string  `gorm:"column:request_hash"`
	PersonalizationMode       string  `gorm:"column:personalization_mode"`
	OnboardingStateAtCreation string  `gorm:"column:onboarding_state_at_creation"`
	ContextRevision           *int64  `gorm:"column:context_revision"`
	Status                    string  `gorm:"column:status"`
	LeaseOwnerRuntimeID       *string `gorm:"column:lease_owner_runtime_id"`
	LeaseEpoch                int64   `gorm:"column:lease_epoch"`
	LeaseTokenHash            *string `gorm:"column:lease_token_hash"`
	LeaseUntil                *int64  `gorm:"column:lease_until"`
	CreatedAt                 int64   `gorm:"column:created_at"`
	ResponseMeta              string  `gorm:"column:response_meta"`
}

func (s *Service) createFeedBatch(ctx context.Context, c *app.RequestContext) {
	agentIDValue, ok := agentID(c)
	if !ok {
		fail(c, http.StatusUnauthorized, "AGENT_AUTH_REQUIRED", "Agent V2 authentication is required", nil)
		return
	}
	if s.feedClient == nil {
		fail(c, http.StatusServiceUnavailable, "FEED_V2_UNAVAILABLE", "Feed V2 is temporarily unavailable", nil)
		return
	}
	var req createFeedBatchRequest
	if err := decodeBody(c, &req); err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error(), nil)
		return
	}
	if req.ProcessingScope == "" {
		req.ProcessingScope = "default"
	}
	if req.Limit == 0 {
		req.Limit = feedMaxItems
	}
	if req.RuntimeInstanceID == "" || len(req.RuntimeInstanceID) > 128 || req.IdempotencyKey == "" || len(req.IdempotencyKey) > 128 ||
		len(req.ProcessingScope) > 64 || req.Limit < 1 || req.Limit > feedMaxItems || len(req.KnownCardVersions) > 100 {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "runtime_instance_id, idempotency_key, scope, or limit is invalid", nil)
		return
	}
	requestBytes, _ := json.Marshal(req)
	requestHash := hashString(string(requestBytes))
	leaseToken, err := randomToken("eflease_", 24)
	if err != nil {
		fail(c, http.StatusInternalServerError, "TOKEN_GENERATION_FAILED", "could not allocate Feed lease", nil)
		return
	}
	now := time.Now().UnixMilli()
	requestID, _ := randomToken("efreq_", 18)
	var batch feedBatchRow
	created := false
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO feed_consumer_state
			(agent_id, processing_scope, lease_epoch, updated_at)
			VALUES (?, ?, 0, ?) ON CONFLICT (agent_id, processing_scope) DO NOTHING`,
			agentIDValue, req.ProcessingScope, now).Error; err != nil {
			return err
		}
		var state struct {
			ActiveBatchID *int64 `gorm:"column:active_batch_id"`
		}
		if err := tx.Raw(`SELECT active_batch_id FROM feed_consumer_state
			WHERE agent_id = ? AND processing_scope = ? FOR UPDATE`, agentIDValue, req.ProcessingScope).Scan(&state).Error; err != nil {
			return err
		}
		if state.ActiveBatchID != nil {
			if err := tx.Raw(`SELECT batch_id, idempotency_key, request_hash, personalization_mode,
				onboarding_state_at_creation, context_revision, status, lease_owner_runtime_id,
				lease_epoch, lease_token_hash, lease_until, created_at, response_meta::text AS response_meta
				FROM feed_batches WHERE batch_id = ? AND agent_id = ? FOR UPDATE`, *state.ActiveBatchID, agentIDValue).Scan(&batch).Error; err != nil {
				return err
			}
			if batch.Status == "building" && batch.CreatedAt+int64(feedBuildStaleAfter/time.Millisecond) > now {
				return errFeedBuilding
			}
			if batch.Status == "building" {
				if err := tx.Exec(`UPDATE feed_batches SET status = 'dead' WHERE batch_id = ?`, batch.BatchID).Error; err != nil {
					return err
				}
				state.ActiveBatchID = nil
			} else if batch.Status == "leased" && batch.LeaseUntil != nil && *batch.LeaseUntil > now &&
				(batch.LeaseOwnerRuntimeID == nil || *batch.LeaseOwnerRuntimeID != req.RuntimeInstanceID) {
				return errFeedLeaseHeld
			} else if batch.Status == "ready" || batch.Status == "partial" || batch.Status == "leased" {
				if batch.IdempotencyKey == req.IdempotencyKey && batch.RequestHash != requestHash {
					return errConflict
				}
				leaseUntil := now + int64(feedLeaseTTL/time.Millisecond)
				maxUntil := batch.CreatedAt + int64(feedMaxLeaseAge/time.Millisecond)
				if leaseUntil > maxUntil {
					leaseUntil = maxUntil
				}
				if leaseUntil <= now {
					if err := tx.Exec(`UPDATE feed_batches SET status = 'expired' WHERE batch_id = ?`, batch.BatchID).Error; err != nil {
						return err
					}
					state.ActiveBatchID = nil
				} else {
					batch.LeaseEpoch++
					batch.LeaseUntil = &leaseUntil
					batch.Status = "leased"
					batch.LeaseOwnerRuntimeID = &req.RuntimeInstanceID
					hash := hashString(leaseToken)
					batch.LeaseTokenHash = &hash
					return tx.Exec(`UPDATE feed_batches SET status = 'leased', lease_owner_runtime_id = ?,
						lease_epoch = ?, lease_token_hash = ?, lease_until = ?, attempt_count = attempt_count + 1
						WHERE batch_id = ?`, req.RuntimeInstanceID, batch.LeaseEpoch, hash, leaseUntil, batch.BatchID).Error
				}
			}
			if state.ActiveBatchID != nil {
				return errConflict
			}
			if err := tx.Exec(`UPDATE feed_consumer_state SET active_batch_id = NULL, updated_at = ?
				WHERE agent_id = ? AND processing_scope = ?`, now, agentIDValue, req.ProcessingScope).Error; err != nil {
				return err
			}
		}

		var onboarding struct {
			State           string `gorm:"column:state"`
			ContextRevision *int64 `gorm:"column:active_context_revision"`
		}
		if err := tx.Raw(`SELECT state, active_context_revision FROM agent_onboarding_v2
			WHERE agent_id = ?`, agentIDValue).Scan(&onboarding).Error; err != nil {
			return err
		}
		if onboarding.State == "" {
			return errUnauthorized
		}
		mode := "baseline"
		if onboarding.State == "completed" {
			mode = "intent_aligned"
		}
		if err := tx.Raw(`INSERT INTO feed_batches
			(agent_id, processing_scope, request_id, idempotency_key, request_hash,
			 personalization_mode, onboarding_state_at_creation, context_revision, status,
			 response_meta, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'building', '{}'::jsonb, ?) RETURNING batch_id,
			 idempotency_key, request_hash, personalization_mode, onboarding_state_at_creation,
			 context_revision, status, created_at, response_meta::text AS response_meta`,
			agentIDValue, req.ProcessingScope, requestID, req.IdempotencyKey, requestHash, mode,
			onboarding.State, onboarding.ContextRevision, now).Scan(&batch).Error; err != nil {
			if isUniqueViolation(err) {
				return errConflict
			}
			return err
		}
		created = true
		return tx.Exec(`UPDATE feed_consumer_state SET active_batch_id = ?, updated_at = ?
			WHERE agent_id = ? AND processing_scope = ?`, batch.BatchID, now, agentIDValue, req.ProcessingScope).Error
	})
	if errors.Is(err, errFeedBuilding) {
		reply(c, http.StatusAccepted, map[string]interface{}{"status": "building", "batch_id": fmt.Sprintf("%d", batch.BatchID)})
		return
	}
	if errors.Is(err, errFeedLeaseHeld) {
		fail(c, http.StatusConflict, "FEED_BATCH_LEASED", "another runtime currently owns this Feed batch", nil)
		return
	}
	if errors.Is(err, errConflict) {
		fail(c, http.StatusConflict, "FEED_BATCH_CONFLICT", "an active or idempotent Feed batch conflicts with this request", nil)
		return
	}
	if errors.Is(err, errUnauthorized) {
		fail(c, http.StatusUnauthorized, "AGENT_AUTH_INVALID", "Agent V2 identity is unavailable", nil)
		return
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, "FEED_BATCH_FAILED", "could not create Feed batch", nil)
		return
	}
	if !created {
		s.replyFeedBatch(c, agentIDValue, batch, leaseToken)
		return
	}

	action := "refresh"
	feedResp, rpcErr := s.feedClient.FetchFeed(ctx, &feedrpc.FetchFeedReq{
		AgentId: agentIDValue, Action: &action, Limit: &req.Limit,
	})
	if rpcErr != nil || feedResp == nil || feedResp.BaseResp == nil || feedResp.BaseResp.Code != 0 {
		s.abandonFeedBatch(agentIDValue, req.ProcessingScope, batch.BatchID)
		fail(c, http.StatusServiceUnavailable, "FEED_SOURCE_UNAVAILABLE", "could not fetch Feed source data", nil)
		return
	}
	payloads, encodeErr := s.buildFeedPayloads(feedResp.Items)
	if encodeErr != nil {
		s.abandonFeedBatch(agentIDValue, req.ProcessingScope, batch.BatchID)
		fail(c, http.StatusInternalServerError, "FEED_BATCH_FAILED", "could not encode Feed batch", nil)
		return
	}
	metaBytes, _ := json.Marshal(map[string]interface{}{
		"has_more": feedResp.HasMore, "impression_id": feedResp.ImpressionId,
	})
	itemsBytes, _ := json.Marshal(payloads)
	leaseUntil := now + int64(feedLeaseTTL/time.Millisecond)
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var activeID *int64
		if err := tx.Raw(`SELECT active_batch_id FROM feed_consumer_state
			WHERE agent_id = ? AND processing_scope = ? FOR UPDATE`, agentIDValue, req.ProcessingScope).Scan(&activeID).Error; err != nil {
			return err
		}
		if activeID == nil || *activeID != batch.BatchID {
			return errConflict
		}
		if len(payloads) > 0 {
			if err := tx.Exec(`INSERT INTO feed_batch_items
				(batch_id, ordinal, source_type, source_id, payload_snapshot, intent_match_snapshot,
				 status, attempt_count, created_at, updated_at)
				SELECT ?, (entry->>'ordinal')::int, entry->>'source_type', (entry->>'source_id')::bigint,
				 entry->'payload', NULLIF(entry->'intent_match', 'null'::jsonb), 'pending', 0, ?, ?
				FROM jsonb_array_elements(?::jsonb) AS entry`, batch.BatchID, now, now, string(itemsBytes)).Error; err != nil {
				return err
			}
		}
		batch.LeaseEpoch = 1
		batch.LeaseUntil = &leaseUntil
		batch.LeaseOwnerRuntimeID = &req.RuntimeInstanceID
		batch.Status = "leased"
		batch.ResponseMeta = string(metaBytes)
		hash := hashString(leaseToken)
		batch.LeaseTokenHash = &hash
		return tx.Exec(`UPDATE feed_batches SET status = 'leased', response_meta = ?::jsonb,
			lease_owner_runtime_id = ?, lease_epoch = 1, lease_token_hash = ?, lease_until = ?, attempt_count = 1
			WHERE batch_id = ? AND status = 'building'`, string(metaBytes), req.RuntimeInstanceID,
			hash, leaseUntil, batch.BatchID).Error
	})
	if err != nil {
		s.abandonFeedBatch(agentIDValue, req.ProcessingScope, batch.BatchID)
		fail(c, http.StatusInternalServerError, "FEED_BATCH_FAILED", "could not persist Feed batch", nil)
		return
	}
	s.replyFeedBatch(c, agentIDValue, batch, leaseToken)
}

type frozenFeedItem struct {
	Ordinal     int                    `json:"ordinal"`
	SourceType  string                 `json:"source_type"`
	SourceID    int64                  `json:"source_id"`
	Payload     map[string]interface{} `json:"payload"`
	IntentMatch interface{}            `json:"intent_match"`
}

type identityAssertion struct {
	SubjectType       string `json:"subject_type"`
	SubjectID         string `json:"subject_id"`
	DisplayName       string `json:"display_name"`
	VerificationLevel string `json:"verification_level"`
}

func (s *Service) resolveIdentityAssertions(agentIDs []int64) (map[int64]identityAssertion, error) {
	result := make(map[int64]identityAssertion, len(agentIDs))
	if len(agentIDs) == 0 {
		return result, nil
	}
	var rows []struct {
		AgentID       int64  `gorm:"column:agent_id"`
		AgentName     string `gorm:"column:agent_name"`
		IsOfficial    bool   `gorm:"column:is_official"`
		EmailVerified bool   `gorm:"column:email_verified"`
	}
	if err := s.db.Raw(`SELECT a.agent_id, a.agent_name, a.is_official,
		EXISTS (SELECT 1 FROM agent_email_bindings b WHERE b.agent_id = a.agent_id
		 AND b.status = 'active' AND b.verification_state = 'verified') AS email_verified
		FROM agents a WHERE a.agent_id = ANY(?)`, pq.Array(agentIDs)).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		level := "unverified"
		if row.EmailVerified {
			level = "email_verified"
		}
		if row.IsOfficial {
			level = "official"
		}
		result[row.AgentID] = identityAssertion{
			SubjectType: "agent", SubjectID: fmt.Sprintf("%d", row.AgentID),
			DisplayName: row.AgentName, VerificationLevel: level,
		}
	}
	return result, nil
}

func (s *Service) buildFeedPayloads(items []*feedrpc.FeedItem) ([]frozenFeedItem, error) {
	authorSet := make(map[int64]struct{})
	for _, item := range items {
		if item.AuthorAgentId != nil && item.SourceType != nil && !strings.EqualFold(*item.SourceType, "pgc") {
			authorSet[*item.AuthorAgentId] = struct{}{}
		}
	}
	authorIDs := make([]int64, 0, len(authorSet))
	for id := range authorSet {
		authorIDs = append(authorIDs, id)
	}
	identities, err := s.resolveIdentityAssertions(authorIDs)
	if err != nil {
		return nil, err
	}
	out := make([]frozenFeedItem, 0, len(items))
	for index, item := range items {
		sourceType := "broadcast"
		if item.SourceType != nil && *item.SourceType != "" {
			sourceType = *item.SourceType
		}
		payload := map[string]interface{}{
			"source_ref":     map[string]interface{}{"type": sourceType, "id": fmt.Sprintf("%d", item.ItemId)},
			"broadcast_type": item.BroadcastType,
			"domains":        nonNilStrings(item.Domains), "keywords": nonNilStrings(item.Keywords),
			"updated_at": item.UpdatedAt,
		}
		if item.Summary != nil {
			payload["summary"] = *item.Summary
		}
		if item.ExpectedResponse != nil {
			payload["expected_response"] = *item.ExpectedResponse
		}
		if item.Suggestion != nil {
			payload["suggestion"] = *item.Suggestion
		}
		if item.RawUrl != nil {
			payload["url"] = *item.RawUrl
		}
		if item.RawContent != nil {
			payload["raw_content"] = *item.RawContent
		}
		if item.RawContentTruncated != nil {
			payload["raw_content_truncated"] = *item.RawContentTruncated
		}
		if item.AuthorAgentId != nil && !strings.EqualFold(sourceType, "pgc") {
			if identity, exists := identities[*item.AuthorAgentId]; exists {
				payload["author_identity"] = identity
			}
		}
		out = append(out, frozenFeedItem{Ordinal: index, SourceType: sourceType, SourceID: item.ItemId, Payload: payload, IntentMatch: nil})
	}
	return out, nil
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func (s *Service) abandonFeedBatch(agentID int64, scope string, batchID int64) {
	now := time.Now().UnixMilli()
	_ = s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`UPDATE feed_batches SET status = 'dead' WHERE batch_id = ? AND status = 'building'`, batchID).Error; err != nil {
			return err
		}
		return tx.Exec(`UPDATE feed_consumer_state SET active_batch_id = NULL, updated_at = ?
			WHERE agent_id = ? AND processing_scope = ? AND active_batch_id = ?`, now, agentID, scope, batchID).Error
	})
}

func (s *Service) replyFeedBatch(c *app.RequestContext, agentID int64, batch feedBatchRow, leaseToken string) {
	var itemRows []struct {
		BatchItemID int64   `gorm:"column:batch_item_id"`
		Ordinal     int     `gorm:"column:ordinal"`
		Payload     string  `gorm:"column:payload_snapshot"`
		IntentMatch *string `gorm:"column:intent_match_snapshot"`
	}
	if err := s.db.Raw(`SELECT batch_item_id, ordinal, payload_snapshot::text AS payload_snapshot,
		intent_match_snapshot::text AS intent_match_snapshot FROM feed_batch_items
		WHERE batch_id = ? ORDER BY ordinal`, batch.BatchID).Scan(&itemRows).Error; err != nil {
		fail(c, http.StatusInternalServerError, "FEED_BATCH_READ_FAILED", "could not read Feed batch", nil)
		return
	}
	items := make([]map[string]interface{}, 0, len(itemRows))
	for _, row := range itemRows {
		var payload map[string]interface{}
		if json.Unmarshal([]byte(row.Payload), &payload) != nil {
			fail(c, http.StatusInternalServerError, "FEED_BATCH_READ_FAILED", "could not decode Feed batch", nil)
			return
		}
		payload["batch_item_id"] = fmt.Sprintf("%d", row.BatchItemID)
		if row.IntentMatch != nil {
			var match interface{}
			_ = json.Unmarshal([]byte(*row.IntentMatch), &match)
			payload["intent_match"] = match
		} else {
			payload["intent_match"] = nil
		}
		items = append(items, payload)
	}
	var meta map[string]interface{}
	_ = json.Unmarshal([]byte(batch.ResponseMeta), &meta)
	var controlContext interface{}
	if batch.ContextRevision != nil {
		var raw string
		if err := s.db.Raw(`SELECT compiled_context::text FROM agent_context_revisions
			WHERE agent_id = ? AND revision = ?`, agentID, *batch.ContextRevision).Scan(&raw).Error; err != nil || json.Unmarshal([]byte(raw), &controlContext) != nil {
			fail(c, http.StatusInternalServerError, "FEED_CONTEXT_READ_FAILED", "could not read frozen control context", nil)
			return
		}
	}
	reply(c, http.StatusOK, map[string]interface{}{
		"batch_id": fmt.Sprintf("%d", batch.BatchID), "status": batch.Status,
		"lease_epoch": batch.LeaseEpoch, "lease_token": leaseToken, "lease_until": batch.LeaseUntil,
		"personalization_mode": batch.PersonalizationMode,
		"onboarding_state":     batch.OnboardingStateAtCreation,
		"context_revision":     batch.ContextRevision, "control_context": controlContext,
		"items": items, "has_more": meta["has_more"], "impression_id": meta["impression_id"],
	})
}

type renewFeedLeaseRequest struct {
	RuntimeInstanceID string `json:"runtime_instance_id"`
	LeaseEpoch        int64  `json:"lease_epoch"`
	LeaseToken        string `json:"lease_token"`
}

func (s *Service) renewFeedLease(_ context.Context, c *app.RequestContext) {
	agentIDValue, _ := agentID(c)
	batchID, err := strconv.ParseInt(c.Param("batch_id"), 10, 64)
	var req renewFeedLeaseRequest
	if err != nil || decodeBody(c, &req) != nil || req.RuntimeInstanceID == "" || req.LeaseEpoch <= 0 || req.LeaseToken == "" {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "batch_id and current lease proof are required", nil)
		return
	}
	now := time.Now().UnixMilli()
	var leaseUntil int64
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var row feedBatchRow
		if err := tx.Raw(`SELECT batch_id, status, lease_owner_runtime_id, lease_epoch,
			lease_token_hash, lease_until, created_at FROM feed_batches
			WHERE batch_id = ? AND agent_id = ? FOR UPDATE`, batchID, agentIDValue).Scan(&row).Error; err != nil {
			return err
		}
		if row.BatchID == 0 || (row.Status != "leased" && row.Status != "partial") || row.LeaseOwnerRuntimeID == nil ||
			*row.LeaseOwnerRuntimeID != req.RuntimeInstanceID || row.LeaseEpoch != req.LeaseEpoch ||
			row.LeaseTokenHash == nil || *row.LeaseTokenHash != hashString(req.LeaseToken) {
			return errConflict
		}
		leaseUntil = now + int64(feedLeaseTTL/time.Millisecond)
		maxUntil := row.CreatedAt + int64(feedMaxLeaseAge/time.Millisecond)
		if leaseUntil > maxUntil {
			leaseUntil = maxUntil
		}
		if leaseUntil <= now {
			return errConflict
		}
		return tx.Exec(`UPDATE feed_batches SET lease_until = ? WHERE batch_id = ? AND lease_epoch = ?`, leaseUntil, batchID, req.LeaseEpoch).Error
	})
	if errors.Is(err, errConflict) {
		fail(c, http.StatusConflict, "LEASE_FENCED", "Feed lease is stale, expired, or owned by another runtime", nil)
		return
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, "LEASE_RENEW_FAILED", "could not renew Feed lease", nil)
		return
	}
	reply(c, http.StatusOK, map[string]interface{}{"batch_id": fmt.Sprintf("%d", batchID), "lease_epoch": req.LeaseEpoch, "lease_until": leaseUntil})
}

type feedItemResult struct {
	BatchItemID string `json:"batch_item_id"`
	Status      string `json:"status"`
	LastError   string `json:"last_error,omitempty"`
}

type ackFeedBatchRequest struct {
	LeaseEpoch     int64            `json:"lease_epoch"`
	LeaseToken     string           `json:"lease_token"`
	IdempotencyKey string           `json:"idempotency_key"`
	ItemResults    []feedItemResult `json:"item_results"`
}

func (s *Service) ackFeedBatch(_ context.Context, c *app.RequestContext) {
	agentIDValue, _ := agentID(c)
	batchID, err := strconv.ParseInt(c.Param("batch_id"), 10, 64)
	var req ackFeedBatchRequest
	if err != nil || decodeBody(c, &req) != nil || req.LeaseEpoch <= 0 || req.LeaseToken == "" || req.IdempotencyKey == "" || len(req.ItemResults) > feedMaxItems {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "batch_id, lease proof, and idempotency_key are required", nil)
		return
	}
	normalized := make([]map[string]interface{}, 0, len(req.ItemResults))
	for _, item := range req.ItemResults {
		id, parseErr := strconv.ParseInt(item.BatchItemID, 10, 64)
		if parseErr != nil || id <= 0 || len(item.LastError) > 1000 {
			fail(c, http.StatusBadRequest, "INVALID_ITEM_RESULT", "item result is invalid", nil)
			return
		}
		switch item.Status {
		case "processed", "skipped", "retryable_failed", "terminal_failed":
		default:
			fail(c, http.StatusBadRequest, "INVALID_ITEM_RESULT", "unsupported item result status", nil)
			return
		}
		normalized = append(normalized, map[string]interface{}{"batch_item_id": id, "status": item.Status, "last_error": item.LastError})
	}
	resultsJSON, _ := json.Marshal(normalized)
	requestHash := hashString(fmt.Sprintf("%d:%s", req.LeaseEpoch, resultsJSON))
	operation := fmt.Sprintf("feed_ack:%d", batchID)
	response := map[string]interface{}{}
	now := time.Now().UnixMilli()
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var prior struct{ RequestHash, Response string }
		if err := tx.Raw(`SELECT request_hash, response_snapshot::text AS response FROM agent_idempotency_requests
			WHERE agent_id = ? AND operation = ? AND idempotency_key = ?`, agentIDValue, operation, req.IdempotencyKey).Scan(&prior).Error; err != nil {
			return err
		}
		if prior.RequestHash != "" {
			if prior.RequestHash != requestHash {
				return errConflict
			}
			return json.Unmarshal([]byte(prior.Response), &response)
		}
		var batch feedBatchRow
		if err := tx.Raw(`SELECT batch_id, processing_scope, status, lease_epoch, lease_token_hash
			FROM feed_batches WHERE batch_id = ? AND agent_id = ? FOR UPDATE`, batchID, agentIDValue).Scan(&batch).Error; err != nil {
			return err
		}
		if batch.BatchID == 0 || (batch.Status != "leased" && batch.Status != "partial") || batch.LeaseEpoch != req.LeaseEpoch ||
			batch.LeaseTokenHash == nil || *batch.LeaseTokenHash != hashString(req.LeaseToken) {
			return errConflict
		}
		if len(normalized) == 0 {
			if err := tx.Exec(`UPDATE feed_batch_items SET status = 'processed', updated_at = ?
				WHERE batch_id = ? AND status IN ('pending','retryable_failed')`, now, batchID).Error; err != nil {
				return err
			}
		} else if err := tx.Exec(`UPDATE feed_batch_items AS item SET status = result.status,
			last_error = NULLIF(result.last_error, ''), attempt_count = item.attempt_count + 1, updated_at = ?
			FROM jsonb_to_recordset(?::jsonb) AS result(batch_item_id bigint, status text, last_error text)
			WHERE item.batch_id = ? AND item.batch_item_id = result.batch_item_id`, now, string(resultsJSON), batchID).Error; err != nil {
			return err
		}
		var unresolved int64
		if err := tx.Raw(`SELECT COUNT(*) FROM feed_batch_items
			WHERE batch_id = ? AND status IN ('pending','retryable_failed')`, batchID).Scan(&unresolved).Error; err != nil {
			return err
		}
		status := "partial"
		if unresolved == 0 {
			status = "acked"
		}
		if err := tx.Exec(`UPDATE feed_batches SET status = ?, acked_at = CASE WHEN ? = 'acked' THEN ? ELSE acked_at END
			WHERE batch_id = ?`, status, status, now, batchID).Error; err != nil {
			return err
		}
		if status == "acked" {
			if err := tx.Exec(`UPDATE feed_consumer_state SET active_batch_id = NULL, updated_at = ?
				WHERE agent_id = ? AND active_batch_id = ?`, now, agentIDValue, batchID).Error; err != nil {
				return err
			}
		}
		response = map[string]interface{}{"batch_id": fmt.Sprintf("%d", batchID), "status": status, "remaining_items": unresolved}
		snapshot, _ := json.Marshal(response)
		return tx.Exec(`INSERT INTO agent_idempotency_requests
			(agent_id, operation, idempotency_key, request_hash, response_snapshot, expires_at, created_at)
			VALUES (?, ?, ?, ?, ?::jsonb, ?, ?)`, agentIDValue, operation, req.IdempotencyKey,
			requestHash, string(snapshot), now+int64(24*time.Hour/time.Millisecond), now).Error
	})
	if errors.Is(err, errConflict) {
		fail(c, http.StatusConflict, "LEASE_FENCED", "Feed lease is stale or idempotency key conflicts", nil)
		return
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, "FEED_ACK_FAILED", "could not acknowledge Feed batch", nil)
		return
	}
	reply(c, http.StatusOK, response)
}
