package consolev2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/lib/pq"
	"gorm.io/gorm"

	feedrpc "eigenflux_server/kitex_gen/eigenflux/feed"
	"eigenflux_server/pkg/activity"
	"eigenflux_server/pkg/agentidentity"
	"eigenflux_server/pkg/feedcontract"
	profiledal "eigenflux_server/rpc/profile/dal"
)

const (
	feedMaxItems       = 20
	feedPayloadBudget  = 192 << 10
	feedResponseBudget = 256 << 10
)

type pullFeedRequest struct {
	Limit                  int32            `json:"limit"`
	KnownCardVersions      map[string]int64 `json:"known_public_card_versions,omitempty"`
	ContextRevisionApplied *int64           `json:"context_revision_applied,omitempty"`
}

// pullFeedV2 is deliberately stateless with respect to delivery. Feed is a
// time-sensitive signal, not a durable command queue: every poll returns the
// latest view, and a busy host may coalesce an older pending view into a newer
// one. Durable execution remains in agent_commands and the domain idempotency
// layers for PM, broadcast, and trade actions.
func (s *Service) pullFeedV2(ctx context.Context, c *app.RequestContext) {
	agentIDValue, ok := agentID(c)
	if !ok {
		fail(c, http.StatusUnauthorized, "AGENT_AUTH_REQUIRED", "Agent V2 authentication is required", nil)
		return
	}
	if s.feedClient == nil {
		fail(c, http.StatusServiceUnavailable, "FEED_V2_UNAVAILABLE", "Feed V2 is temporarily unavailable", nil)
		return
	}
	var req pullFeedRequest
	if err := decodeBody(c, &req); err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error(), nil)
		return
	}
	if req.Limit == 0 {
		req.Limit = feedMaxItems
	}
	if req.Limit < 1 || req.Limit > feedMaxItems || len(req.KnownCardVersions) > 100 ||
		(req.ContextRevisionApplied != nil && *req.ContextRevisionApplied < 0) {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "limit, known Card versions, or context revision is invalid", nil)
		return
	}
	var onboarding struct {
		State               string `gorm:"column:state"`
		ContextRevision     *int64 `gorm:"column:active_context_revision"`
		PollIntervalSeconds int    `gorm:"column:poll_interval_seconds"`
	}
	if err := s.db.Raw(`SELECT o.state, o.active_context_revision,
		COALESCE(settings.poll_interval_seconds, 600) AS poll_interval_seconds
		FROM agent_onboarding_v2 o
		LEFT JOIN agent_feed_v2_settings settings ON settings.agent_id = o.agent_id
		WHERE o.agent_id = ?`, agentIDValue).Scan(&onboarding).Error; err != nil {
		fail(c, http.StatusInternalServerError, "FEED_READ_FAILED", "could not load Feed personalization", nil)
		return
	}
	if onboarding.State == "" {
		fail(c, http.StatusUnauthorized, "AGENT_AUTH_INVALID", "Agent V2 identity is unavailable", nil)
		return
	}
	mode := "baseline"
	if onboarding.State == "completed" {
		mode = "intent_aligned"
		if onboarding.ContextRevision == nil || *onboarding.ContextRevision <= 0 {
			fail(c, http.StatusInternalServerError, "FEED_CONTEXT_READ_FAILED", "completed onboarding has no active control context", nil)
			return
		}
	}

	action := "refresh"
	feedResp, rpcErr := s.feedClient.FetchFeed(ctx, &feedrpc.FetchFeedReq{
		AgentId: agentIDValue, Action: &action, Limit: &req.Limit,
	})
	if rpcErr != nil || feedResp == nil || feedResp.BaseResp == nil || feedResp.BaseResp.Code != 0 {
		fail(c, http.StatusServiceUnavailable, "FEED_SOURCE_UNAVAILABLE", "could not fetch Feed source data", nil)
		return
	}
	payloads, cardUpdates, encodeErr := s.buildFeedPayloads(agentIDValue, mode,
		onboarding.ContextRevision, feedResp.Items, req.KnownCardVersions)
	if encodeErr != nil {
		fail(c, http.StatusInternalServerError, "FEED_READ_FAILED", "could not encode Feed response", nil)
		return
	}

	contextDelivery := "none"
	if onboarding.ContextRevision != nil {
		contextDelivery = "full"
		if req.ContextRevisionApplied != nil && *req.ContextRevisionApplied == *onboarding.ContextRevision {
			contextDelivery = "unchanged"
		}
	}
	itemsBytes, _ := json.Marshal(payloads)
	if len(itemsBytes) > feedPayloadBudget {
		shrinkFeedPayloads(payloads)
		itemsBytes, _ = json.Marshal(payloads)
	}
	if len(itemsBytes) > feedPayloadBudget {
		fail(c, http.StatusServiceUnavailable, "FEED_PAYLOAD_TOO_LARGE", "Feed response exceeds the V2 response budget", nil)
		return
	}
	var now int64
	if err := s.db.Raw(`SELECT (extract(epoch FROM clock_timestamp())*1000)::bigint`).Scan(&now).Error; err != nil {
		fail(c, http.StatusInternalServerError, "FEED_READ_FAILED", "could not timestamp Feed response", nil)
		return
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if len(payloads) > 0 {
			if err := persistFeedExposures(tx, agentIDValue, payloads, onboarding.ContextRevision, now); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		fail(c, http.StatusInternalServerError, "FEED_READ_FAILED", "could not record Feed exposure", nil)
		return
	}

	var controlContext interface{}
	if onboarding.ContextRevision != nil && contextDelivery == "full" {
		var raw string
		if err := s.db.Raw(`SELECT compiled_context::text FROM agent_context_revisions
			WHERE agent_id = ? AND revision = ?`, agentIDValue, *onboarding.ContextRevision).Scan(&raw).Error; err != nil || json.Unmarshal([]byte(raw), &controlContext) != nil {
			fail(c, http.StatusInternalServerError, "FEED_CONTEXT_READ_FAILED", "could not read control context", nil)
			return
		}
	}
	items := make([]map[string]interface{}, 0, len(payloads))
	for _, item := range payloads {
		payload := item.Payload
		payload["intent_match"] = item.IntentMatch
		items = append(items, payload)
	}
	response := map[string]interface{}{
		"schema_version": "feed.v2", "impression_id": feedResp.ImpressionId,
		"personalization": map[string]interface{}{
			"mode": mode, "onboarding_state": onboarding.State,
			"context_revision": onboarding.ContextRevision, "context_delivery": contextDelivery,
		},
		"control_context_snapshot": controlContext,
		"agent_card_updates":       cardUpdates,
		"cadence": map[string]interface{}{
			"poll_interval_seconds": onboarding.PollIntervalSeconds,
			"phase_seconds":         agentIDValue % int64(onboarding.PollIntervalSeconds),
		},
		"items": items, "notifications": []interface{}{},
		"next_cursor": nil, "has_more": feedResp.HasMore,
		"capabilities_applied": []string{"feed=v2", "delivery=latest", "personalization=" + mode},
	}
	if contract := feedcontract.Default(); contract != "" {
		response["output_contract"] = contract
	}
	encoded, _ := json.Marshal(response)
	if len(encoded) > feedResponseBudget {
		response["agent_card_updates"] = map[string]interface{}{}
		encoded, _ = json.Marshal(response)
	}
	if len(encoded) > feedResponseBudget {
		fail(c, http.StatusServiceUnavailable, "FEED_PAYLOAD_TOO_LARGE", "Feed response exceeds the V2 response budget", nil)
		return
	}
	activity.PublishFeedPull(ctx, agentIDValue, len(feedResp.Items))
	reply(c, http.StatusOK, response)
	activity.PublishFeedPull(ctx, agentIDValue, len(items))
}

type feedExposureSeed struct {
	SourceType    string `json:"source_type"`
	SourceID      int64  `json:"source_id"`
	ContentClass  string `json:"content_class"`
	AuthorAgentID *int64 `json:"author_agent_id,omitempty"`
}

// persistFeedExposures records only the compact projection needed by Today
// and source-detail authorization. It has no delivery or processing state.
func persistFeedExposures(tx *gorm.DB, agentID int64, items []frozenFeedItem, contextRevision *int64, now int64) error {
	seeds := make([]feedExposureSeed, 0, len(items))
	for _, item := range items {
		contentClass, _ := item.Payload["content_class"].(string)
		var authorAgentID *int64
		if identity, ok := item.Payload["author_identity"].(map[string]interface{}); ok {
			if rawID, ok := identity["agent_id"].(string); ok {
				if parsed, err := strconv.ParseInt(rawID, 10, 64); err == nil && parsed > 0 {
					authorAgentID = &parsed
				}
			}
		}
		seeds = append(seeds, feedExposureSeed{
			SourceType: item.SourceType, SourceID: item.SourceID,
			ContentClass: contentClass, AuthorAgentID: authorAgentID,
		})
	}
	if len(seeds) == 0 {
		return nil
	}
	encoded, err := json.Marshal(seeds)
	if err != nil {
		return err
	}
	return tx.Exec(`INSERT INTO agent_feed_exposures
		(agent_id, source_type, source_id, content_class, author_agent_id,
		 context_revision, first_seen_at, last_seen_at, seen_count)
		SELECT ?, seed.source_type, seed.source_id, seed.content_class,
		 seed.author_agent_id, ?, ?, ?, 1
		FROM jsonb_to_recordset(?::jsonb) AS seed(
		 source_type text, source_id bigint, content_class text, author_agent_id bigint)
		ON CONFLICT (agent_id, source_type, source_id) DO UPDATE SET
		 content_class = EXCLUDED.content_class,
		 author_agent_id = EXCLUDED.author_agent_id,
		 context_revision = EXCLUDED.context_revision,
		 last_seen_at = EXCLUDED.last_seen_at,
		 seen_count = agent_feed_exposures.seen_count + 1`,
		agentID, contextRevision, now, now, string(encoded)).Error
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
	ShortID           string `json:"short_id,omitempty"`
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
		ShortID       string `gorm:"column:short_id"`
		AgentName     string `gorm:"column:agent_name"`
		IsOfficial    bool   `gorm:"column:is_official"`
		EmailVerified bool   `gorm:"column:email_verified"`
	}
	if err := s.db.Raw(`SELECT a.agent_id, a.short_id, a.agent_name, a.is_official,
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
			ShortID: row.ShortID, DisplayName: agentidentity.DisplayName(row.AgentName, row.ShortID), VerificationLevel: level,
		}
	}
	return result, nil
}

type feedIntent struct {
	IntentID          string `json:"intent_id"`
	WatchFor          string `json:"watch_for"`
	TriggerWhen       string `json:"trigger_when"`
	ActionInstruction string `json:"then"`
	ActionPolicy      string `json:"action_policy"`
}

func (s *Service) buildFeedPayloads(viewerID int64, mode string, contextRevision *int64, items []*feedrpc.FeedItem,
	knownVersions map[string]int64) ([]frozenFeedItem, map[string]interface{}, error) {
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
		return nil, nil, err
	}
	cards, cardErr := profiledal.GetAgentCards(s.db, authorIDs)
	cardUpdates := make(map[string]interface{})
	if cardErr == nil {
		for _, authorID := range authorIDs {
			card, exists := cards[authorID]
			if !exists || knownVersions[strconv.FormatInt(authorID, 10)] == card.PublicCardVersion {
				continue
			}
			summary, summaryErr := communicationSummary(card.PublicCard)
			if summaryErr != nil {
				continue
			}
			cardUpdates[strconv.FormatInt(authorID, 10)] = map[string]interface{}{
				"public_card_version": card.PublicCardVersion,
				"card_generated_at":   card.PublicCardGeneratedAt,
				"card_summary":        summary,
			}
		}
	}
	var intents []feedIntent
	if mode == "intent_aligned" {
		if contextRevision == nil {
			return nil, nil, errors.New("intent-aligned Feed batch has no frozen context revision")
		}
		var rawContext string
		if err := s.db.Raw(`SELECT compiled_context::text FROM agent_context_revisions
			WHERE agent_id = ? AND revision = ?`, viewerID, *contextRevision).Scan(&rawContext).Error; err != nil {
			return nil, nil, err
		}
		var snapshot struct {
			IntentActions []feedIntent `json:"intent_actions"`
		}
		decoder := json.NewDecoder(strings.NewReader(rawContext))
		decoder.UseNumber()
		if decoder.Decode(&snapshot) != nil || len(snapshot.IntentActions) > 10 {
			return nil, nil, errors.New("frozen control context is invalid")
		}
		intents = snapshot.IntentActions
	}
	out := make([]frozenFeedItem, 0, len(items))
	for index, item := range items {
		upstreamSourceType := ""
		if item.SourceType != nil && *item.SourceType != "" {
			upstreamSourceType = *item.SourceType
		}
		contentClass := "ugc"
		if strings.EqualFold(upstreamSourceType, "pgc") {
			contentClass = "pgc"
		}
		previewText := ""
		if item.Summary != nil {
			previewText = *item.Summary
		} else if item.RawContent != nil {
			previewText = *item.RawContent
		}
		previewText, previewTruncated := truncateRunes(previewText, 800)
		payload := map[string]interface{}{
			"source_ref":      map[string]interface{}{"type": "broadcast", "id": fmt.Sprintf("%d", item.ItemId)},
			"content_class":   contentClass,
			"author_identity": nil,
			"preview":         map[string]interface{}{"text": previewText, "truncated": previewTruncated},
			"metadata": map[string]interface{}{
				"broadcast_type": item.BroadcastType,
				"domains":        boundedFeedStrings(item.Domains, 8, 64),
				"keywords":       boundedFeedStrings(item.Keywords, 12, 64),
				"source_type":    upstreamSourceType,
				"updated_at":     item.UpdatedAt,
			},
			"source_expectation":  "",
			"recommended_actions": []interface{}{},
			"entity_refs":         []interface{}{},
		}
		if item.ExpectedResponse != nil {
			expectation, _ := truncateRunes(*item.ExpectedResponse, 500)
			payload["source_expectation"] = expectation
		}
		if item.AuthorAgentId != nil && contentClass == "ugc" {
			if identity, exists := identities[*item.AuthorAgentId]; exists {
				payload["author_identity"] = map[string]interface{}{
					"agent_id": identity.SubjectID, "agent_name": identity.DisplayName,
					"short_id":           identity.ShortID,
					"verification_level": identity.VerificationLevel,
				}
				payload["entity_refs"] = []interface{}{map[string]interface{}{
					"type": "agent", "id": identity.SubjectID,
				}}
			}
			if card, exists := cards[*item.AuthorAgentId]; exists && cardErr == nil {
				payload["author_public_card_version"] = card.PublicCardVersion
			}
			relation := "stranger"
			if item.AuthorRelation != nil && strings.EqualFold(*item.AuthorRelation, "friend") {
				relation = "friend"
			}
			payload["author_relation"] = relation
		}
		intentMatch := interface{}(nil)
		if mode == "intent_aligned" {
			match, actions := matchFeedIntents(viewerID, contextRevision, item, intents)
			intentMatch = match
			payload["recommended_actions"] = actions
		}
		out = append(out, frozenFeedItem{Ordinal: index, SourceType: "broadcast", SourceID: item.ItemId, Payload: payload, IntentMatch: intentMatch})
	}
	return out, cardUpdates, nil
}

func matchFeedIntents(viewerID int64, contextRevision *int64, item *feedrpc.FeedItem, intents []feedIntent) (map[string]interface{}, []interface{}) {
	haystackParts := []string{item.BroadcastType}
	if item.Summary != nil {
		haystackParts = append(haystackParts, *item.Summary)
	}
	if item.ExpectedResponse != nil {
		haystackParts = append(haystackParts, *item.ExpectedResponse)
	}
	haystackParts = append(haystackParts, item.Domains...)
	haystackParts = append(haystackParts, item.Keywords...)
	haystack := strings.ToLower(strings.Join(haystackParts, " "))
	matchedIDs := make([]string, 0, len(intents))
	actions := make([]interface{}, 0, len(intents))
	bestScore := float64(0)
	revision := int64(0)
	if contextRevision != nil {
		revision = *contextRevision
	}
	for _, intent := range intents {
		terms := intentTerms(intent.WatchFor + " " + intent.TriggerWhen)
		if len(terms) == 0 {
			continue
		}
		matchedTerms := 0
		for _, term := range terms {
			if strings.Contains(haystack, term) {
				matchedTerms++
			}
		}
		if matchedTerms == 0 {
			continue
		}
		score := float64(matchedTerms) / float64(len(terms))
		if score > bestScore {
			bestScore = score
		}
		intentID := intent.IntentID
		intentIDNumber, parseErr := strconv.ParseInt(intent.IntentID, 10, 64)
		if parseErr != nil || intentIDNumber <= 0 {
			continue
		}
		matchedIDs = append(matchedIDs, intentID)
		actionType := "research"
		requiresConfirmation := false
		switch intent.ActionPolicy {
		case "draft":
			actionType = "draft"
		case "network_action":
			actionType, requiresConfirmation = "network_action", true
		case "trade_action":
			actionType, requiresConfirmation = "trade_action", true
		}
		actions = append(actions, map[string]interface{}{
			"action_idempotency_key": "feed_" + hashString(fmt.Sprintf("%d:%d:%d:%d", viewerID, item.ItemId, intentIDNumber, revision))[:24],
			"type":                   actionType, "instruction": intent.ActionInstruction,
			"policy": intent.ActionPolicy, "requires_user_confirmation": requiresConfirmation,
		})
	}
	status := "unmatched"
	reason := "no confirmed intent matched this item"
	if len(matchedIDs) > 0 {
		status = "matched"
		reason = "matched confirmed intent terms"
	}
	return map[string]interface{}{
		"status": status, "matched_intent_ids": matchedIDs,
		"score": bestScore, "reason": reason,
	}, actions
}

func intentTerms(value string) []string {
	parts := strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r))
	})
	seen := make(map[string]struct{}, len(parts))
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if len([]rune(part)) < 2 {
			continue
		}
		if _, exists := seen[part]; exists {
			continue
		}
		seen[part] = struct{}{}
		out = append(out, part)
		if len(out) == 32 {
			break
		}
	}
	return out
}

func boundedFeedStrings(values []string, maxItems, maxRunes int) []string {
	out := make([]string, 0, min(len(values), maxItems))
	for _, value := range values {
		value, _ = truncateRunes(value, maxRunes)
		if value != "" {
			out = append(out, value)
		}
		if len(out) == maxItems {
			break
		}
	}
	return out
}

func shrinkFeedPayloads(items []frozenFeedItem) {
	for index := range items {
		preview, _ := items[index].Payload["preview"].(map[string]interface{})
		if text, ok := preview["text"].(string); ok {
			shortened, truncated := truncateRunes(text, 300)
			preview["text"], preview["truncated"] = shortened, truncated || preview["truncated"] == true
		}
		if expectation, ok := items[index].Payload["source_expectation"].(string); ok {
			shortened, _ := truncateRunes(expectation, 100)
			items[index].Payload["source_expectation"] = shortened
		}
	}
}
