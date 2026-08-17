package consolev2

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/lib/pq"

	profiledal "eigenflux_server/rpc/profile/dal"
)

const (
	communicationDefaultLimit = 20
	communicationMaxLimit     = 50
	communicationMaxReplySize = 256 << 10
)

type communicationCardSummary struct {
	AgentDescription string   `json:"agent_description"`
	HumanDescription string   `json:"human_description"`
	WorkingLanguages []string `json:"working_languages"`
	Seeking          []string `json:"seeking"`
	Offering         []string `json:"offering"`
	Truncated        bool     `json:"truncated"`
}

type communicationAgentContext struct {
	IdentityAssertion identityAssertion        `json:"identity_assertion"`
	CardSummary       communicationCardSummary `json:"card_summary"`
	PublicCardVersion int64                    `json:"public_card_version"`
	CardGeneratedAt   int64                    `json:"card_generated_at,omitempty"`
	ViewerRelation    string                   `json:"viewer_relation"`
	ProfileStatus     string                   `json:"profile_status"`
}

type communicationMessage struct {
	MsgID            int64  `gorm:"column:msg_id" json:"msg_id,string"`
	ConvID           int64  `gorm:"column:conv_id" json:"conv_id,string"`
	SenderID         int64  `gorm:"column:sender_id" json:"sender_agent_id,string"`
	ReceiverID       int64  `gorm:"column:receiver_id" json:"receiver_agent_id,string"`
	Content          string `gorm:"column:content" json:"content"`
	ContentTruncated bool   `gorm:"-" json:"content_truncated,omitempty"`
	IsRead           bool   `gorm:"column:is_read" json:"is_read"`
	CreatedAt        int64  `gorm:"column:created_at" json:"created_at"`
}

type communicationConversation struct {
	ConvID       int64                 `gorm:"column:conv_id" json:"conv_id,string"`
	ParticipantA int64                 `gorm:"column:participant_a" json:"-"`
	ParticipantB int64                 `gorm:"column:participant_b" json:"-"`
	OriginType   string                `gorm:"column:origin_type" json:"origin_type"`
	OriginID     int64                 `gorm:"column:origin_id" json:"origin_id,string,omitempty"`
	MsgCount     int64                 `gorm:"column:msg_count" json:"msg_count"`
	UpdatedAt    int64                 `gorm:"column:updated_at" json:"updated_at"`
	PeerAgentID  int64                 `json:"peer_agent_id,string"`
	UnreadCount  int64                 `json:"unread_count"`
	Category     string                `gorm:"-" json:"category"`
	LastMessage  *communicationMessage `gorm:"-" json:"last_message,omitempty"`
}

type conversationCursor struct {
	UpdatedAt int64 `json:"u"`
	ConvID    int64 `json:"c"`
}

func parseCommunicationLimit(c *app.RequestContext) (int, error) {
	raw := c.Query("limit")
	if raw == "" {
		return communicationDefaultLimit, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 || limit > communicationMaxLimit {
		return 0, fmt.Errorf("limit must be between 1 and %d", communicationMaxLimit)
	}
	return limit, nil
}

func encodeConversationCursor(value conversationCursor) string {
	raw, _ := json.Marshal(value)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeConversationCursor(raw string) (conversationCursor, error) {
	if raw == "" {
		return conversationCursor{}, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return conversationCursor{}, errors.New("invalid cursor")
	}
	var value conversationCursor
	if json.Unmarshal(decoded, &value) != nil || value.UpdatedAt <= 0 || value.ConvID <= 0 {
		return conversationCursor{}, errors.New("invalid cursor")
	}
	return value, nil
}

func parseIDCursor(raw string) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return 0, errors.New("invalid cursor")
	}
	return value, nil
}

func deduplicateAgentIDs(ids []int64) []int64 {
	out := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func truncateRunes(value string, limit int) (string, bool) {
	if utf8.RuneCountInString(value) <= limit {
		return value, false
	}
	runes := []rune(value)
	return string(runes[:limit]), true
}

func boundCommunicationMessage(message *communicationMessage, runeLimit int) {
	if message == nil {
		return
	}
	message.Content, message.ContentTruncated = truncateRunes(message.Content, runeLimit)
}

func communicationReplyFits(data map[string]interface{}) bool {
	encoded, err := json.Marshal(map[string]interface{}{"data": data})
	return err == nil && len(encoded) <= communicationMaxReplySize
}

func filterCommunicationContexts(contexts map[string]communicationAgentContext, peerIDs []int64) map[string]communicationAgentContext {
	peerIDs = deduplicateAgentIDs(peerIDs)
	filtered := make(map[string]communicationAgentContext, len(peerIDs))
	for _, peerID := range peerIDs {
		key := strconv.FormatInt(peerID, 10)
		if value, exists := contexts[key]; exists {
			filtered[key] = value
		}
	}
	return filtered
}

func decodeCardString(raw map[string]interface{}, key string, limit int) (string, bool) {
	value, _ := raw[key].(string)
	return truncateRunes(value, limit)
}

func decodeCardList(raw map[string]interface{}, key string, maxItems, maxRunes int) ([]string, bool) {
	values, ok := raw[key].([]interface{})
	if !ok {
		return []string{}, raw[key] != nil
	}
	truncated := len(values) > maxItems
	if len(values) > maxItems {
		values = values[:maxItems]
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		text, ok := value.(string)
		if !ok {
			truncated = true
			continue
		}
		text, cut := truncateRunes(text, maxRunes)
		truncated = truncated || cut
		out = append(out, text)
	}
	return out, truncated
}

func communicationSummary(publicCard string) (communicationCardSummary, error) {
	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(publicCard), &raw); err != nil {
		return communicationCardSummary{}, err
	}
	result := communicationCardSummary{}
	var cut bool
	result.AgentDescription, cut = decodeCardString(raw, "agent_description", 300)
	result.Truncated = result.Truncated || cut
	result.HumanDescription, cut = decodeCardString(raw, "human_description", 200)
	result.Truncated = result.Truncated || cut
	result.WorkingLanguages, cut = decodeCardList(raw, "working_languages", 5, 80)
	result.Truncated = result.Truncated || cut
	result.Seeking, cut = decodeCardList(raw, "seeking", 5, 80)
	result.Truncated = result.Truncated || cut
	result.Offering, cut = decodeCardList(raw, "offering", 5, 80)
	result.Truncated = result.Truncated || cut
	return result, nil
}

// loadCommunicationContexts performs a bounded three-query enrichment: fresh
// server identity assertions, one public-card batch read, and one bidirectional
// block lookup. Viewer-relative relation state is supplied by the authorized
// business page and is never cached with the public card.
func (s *Service) loadCommunicationContexts(viewerID int64, peerIDs []int64, relations map[int64]string) (map[string]communicationAgentContext, error) {
	peerIDs = deduplicateAgentIDs(peerIDs)
	result := make(map[string]communicationAgentContext, len(peerIDs))
	if len(peerIDs) == 0 {
		return result, nil
	}
	identities, err := s.resolveIdentityAssertions(peerIDs)
	if err != nil {
		return nil, err
	}
	cards, cardErr := profiledal.GetAgentCards(s.db, peerIDs)

	var blockedPeers []int64
	if err := s.db.Raw(`SELECT DISTINCT CASE WHEN from_uid = ? THEN to_uid ELSE from_uid END AS peer_id
		FROM user_relations
		WHERE rel_type = 2 AND ((from_uid = ? AND to_uid = ANY(?)) OR (to_uid = ? AND from_uid = ANY(?)))`,
		viewerID, viewerID, pq.Array(peerIDs), viewerID, pq.Array(peerIDs)).Scan(&blockedPeers).Error; err != nil {
		return nil, err
	}
	blocked := make(map[int64]bool, len(blockedPeers))
	for _, peerID := range blockedPeers {
		blocked[peerID] = true
	}

	for _, peerID := range peerIDs {
		key := strconv.FormatInt(peerID, 10)
		relation := relations[peerID]
		if relation == "" {
			relation = "none"
		}
		if blocked[peerID] {
			result[key] = communicationAgentContext{
				IdentityAssertion: identityAssertion{SubjectType: "agent", SubjectID: key, DisplayName: "不可用 Agent", VerificationLevel: "unverified"},
				CardSummary:       communicationCardSummary{WorkingLanguages: []string{}, Seeking: []string{}, Offering: []string{}},
				ViewerRelation:    relation, ProfileStatus: "unavailable",
			}
			continue
		}
		identity, exists := identities[peerID]
		if !exists {
			result[key] = communicationAgentContext{
				IdentityAssertion: identityAssertion{SubjectType: "agent", SubjectID: key, DisplayName: "已注销 Agent", VerificationLevel: "unverified"},
				CardSummary:       communicationCardSummary{WorkingLanguages: []string{}, Seeking: []string{}, Offering: []string{}},
				ViewerRelation:    relation, ProfileStatus: "deleted",
			}
			continue
		}
		contextValue := communicationAgentContext{
			IdentityAssertion: identity, ViewerRelation: relation, ProfileStatus: "missing",
			CardSummary: communicationCardSummary{WorkingLanguages: []string{}, Seeking: []string{}, Offering: []string{}},
		}
		if cardErr != nil {
			contextValue.ProfileStatus = "unavailable"
			result[key] = contextValue
			continue
		}
		if card, ok := cards[peerID]; ok {
			summary, decodeErr := communicationSummary(card.PublicCard)
			if decodeErr != nil {
				contextValue.ProfileStatus = "unavailable"
			} else {
				contextValue.CardSummary = summary
				contextValue.PublicCardVersion = card.PublicCardVersion
				contextValue.CardGeneratedAt = card.PublicCardGeneratedAt
				contextValue.ProfileStatus = "available"
			}
		}
		result[key] = contextValue
	}
	return result, nil
}

func (s *Service) listCommunicationConversations(_ context.Context, c *app.RequestContext) {
	viewerID, _ := agentID(c)
	limit, err := parseCommunicationLimit(c)
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_LIMIT", err.Error(), nil)
		return
	}
	cursor, err := decodeConversationCursor(c.Query("cursor"))
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_CURSOR", err.Error(), nil)
		return
	}
	originType := strings.TrimSpace(c.Query("origin_type"))
	if originType != "" && originType != "friend" && originType != "broadcast" && originType != "unbroken" {
		fail(c, http.StatusBadRequest, "INVALID_ORIGIN_TYPE", "origin_type must be friend, broadcast or unbroken", nil)
		return
	}

	filter := ""
	branchArgs := func() []interface{} {
		args := []interface{}{viewerID}
		if originType == "unbroken" {
			args = append(args, viewerID)
		}
		if cursor.UpdatedAt > 0 {
			args = append(args, cursor.UpdatedAt, cursor.ConvID)
		}
		if originType != "" && originType != "unbroken" {
			args = append(args, originType)
		}
		args = append(args, limit+1)
		return args
	}
	if cursor.UpdatedAt > 0 {
		filter += " AND (updated_at, conv_id) < (?, ?)"
	}
	if originType != "" && originType != "unbroken" {
		filter += " AND origin_type = ?"
	}
	baseCondition := "status = 0 AND msg_count >= 1"
	if originType == "unbroken" {
		baseCondition = "status = 0 AND origin_type <> 'friend' AND msg_count = 1 AND last_sender_id <> ?"
	}
	baseA := `SELECT conv_id, participant_a, participant_b, COALESCE(origin_type, '') AS origin_type,
		COALESCE(origin_id, 0) AS origin_id, msg_count, updated_at
		FROM conversations WHERE participant_a = ? AND ` + baseCondition + filter + `
		ORDER BY updated_at DESC, conv_id DESC LIMIT ?`
	baseB := `SELECT conv_id, participant_a, participant_b, COALESCE(origin_type, '') AS origin_type,
		COALESCE(origin_id, 0) AS origin_id, msg_count, updated_at
		FROM conversations WHERE participant_b = ? AND ` + baseCondition + filter + `
		ORDER BY updated_at DESC, conv_id DESC LIMIT ?`
	args := append(branchArgs(), branchArgs()...)
	args = append(args, limit+1)
	query := `SELECT * FROM ((` + baseA + `) UNION ALL (` + baseB + `)) AS page
		ORDER BY updated_at DESC, conv_id DESC LIMIT ?`
	var rows []communicationConversation
	if err := s.db.Raw(query, args...).Scan(&rows).Error; err != nil {
		fail(c, http.StatusInternalServerError, "CONVERSATIONS_READ_FAILED", "could not read conversations", nil)
		return
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	convIDs := make([]int64, 0, len(rows))
	peerIDs := make([]int64, 0, len(rows))
	for index := range rows {
		peerID := rows[index].ParticipantA
		if peerID == viewerID {
			peerID = rows[index].ParticipantB
		}
		rows[index].PeerAgentID = peerID
		convIDs = append(convIDs, rows[index].ConvID)
		peerIDs = append(peerIDs, peerID)
	}
	if len(convIDs) > 0 {
		var lastMessages []communicationMessage
		if err := s.db.Raw(`SELECT DISTINCT ON (conv_id) msg_id, conv_id, sender_id, receiver_id, content, is_read, created_at
			FROM private_messages WHERE conv_id = ANY(?) ORDER BY conv_id, msg_id DESC`, pq.Array(convIDs)).Scan(&lastMessages).Error; err != nil {
			fail(c, http.StatusInternalServerError, "CONVERSATIONS_READ_FAILED", "could not read conversation messages", nil)
			return
		}
		var unreadRows []struct {
			ConvID int64 `gorm:"column:conv_id"`
			Count  int64 `gorm:"column:count"`
		}
		if err := s.db.Raw(`SELECT conv_id, COUNT(*) AS count FROM private_messages
			WHERE conv_id = ANY(?) AND receiver_id = ? AND is_read = false GROUP BY conv_id`, pq.Array(convIDs), viewerID).Scan(&unreadRows).Error; err != nil {
			fail(c, http.StatusInternalServerError, "CONVERSATIONS_READ_FAILED", "could not read unread counts", nil)
			return
		}
		lastByConversation := make(map[int64]*communicationMessage, len(lastMessages))
		for index := range lastMessages {
			boundCommunicationMessage(&lastMessages[index], 1000)
			lastByConversation[lastMessages[index].ConvID] = &lastMessages[index]
		}
		unreadByConversation := make(map[int64]int64, len(unreadRows))
		for _, row := range unreadRows {
			unreadByConversation[row.ConvID] = row.Count
		}
		for index := range rows {
			rows[index].LastMessage = lastByConversation[rows[index].ConvID]
			rows[index].UnreadCount = unreadByConversation[rows[index].ConvID]
		}
	}
	relations, err := s.loadViewerRelations(viewerID, peerIDs)
	if err != nil {
		fail(c, http.StatusInternalServerError, "CONVERSATIONS_READ_FAILED", "could not read relationship state", nil)
		return
	}
	contexts, err := s.loadCommunicationContexts(viewerID, peerIDs, relations)
	if err != nil {
		fail(c, http.StatusInternalServerError, "IDENTITY_READ_FAILED", "could not resolve Agent identities", nil)
		return
	}
	for index := range rows {
		relation := relations[rows[index].PeerAgentID]
		switch {
		case rows[index].OriginType == "friend":
			rows[index].Category = "friend"
		case relation == "friend":
			rows[index].Category = "broadcast_comment"
		default:
			rows[index].Category = "non_friend"
		}
	}
	nextCursor := ""
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		nextCursor = encodeConversationCursor(conversationCursor{UpdatedAt: last.UpdatedAt, ConvID: last.ConvID})
	}
	data := map[string]interface{}{
		"viewer_agent_id": strconv.FormatInt(viewerID, 10), "conversations": rows,
		"agent_contexts": contexts, "next_cursor": nextCursor, "has_more": hasMore,
	}
	for len(rows) > 1 && !communicationReplyFits(data) {
		rows = rows[:len(rows)-1]
		hasMore = true
		peerIDs = peerIDs[:len(rows)]
		contexts = filterCommunicationContexts(contexts, peerIDs)
		last := rows[len(rows)-1]
		nextCursor = encodeConversationCursor(conversationCursor{UpdatedAt: last.UpdatedAt, ConvID: last.ConvID})
		data["conversations"], data["agent_contexts"] = rows, contexts
		data["next_cursor"], data["has_more"] = nextCursor, hasMore
	}
	if !communicationReplyFits(data) {
		fail(c, http.StatusInternalServerError, "COMMUNICATION_RESPONSE_TOO_LARGE", "one conversation cannot fit the V2 response budget", nil)
		return
	}
	reply(c, http.StatusOK, data)
}

func (s *Service) loadViewerRelations(viewerID int64, peerIDs []int64) (map[int64]string, error) {
	peerIDs = deduplicateAgentIDs(peerIDs)
	result := make(map[int64]string, len(peerIDs))
	if len(peerIDs) == 0 {
		return result, nil
	}
	var friendIDs []int64
	if err := s.db.Raw(`SELECT to_uid FROM user_relations
		WHERE from_uid = ? AND rel_type = 1 AND to_uid = ANY(?)`, viewerID, pq.Array(peerIDs)).Scan(&friendIDs).Error; err != nil {
		return nil, err
	}
	for _, peerID := range friendIDs {
		result[peerID] = "friend"
	}
	return result, nil
}

func (s *Service) listCommunicationMessages(_ context.Context, c *app.RequestContext) {
	viewerID, _ := agentID(c)
	limit, err := parseCommunicationLimit(c)
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_LIMIT", err.Error(), nil)
		return
	}
	convID, err := strconv.ParseInt(c.Param("conv_id"), 10, 64)
	if err != nil || convID <= 0 {
		fail(c, http.StatusBadRequest, "INVALID_CONVERSATION", "invalid conv_id", nil)
		return
	}
	cursor, err := parseIDCursor(c.Query("cursor"))
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_CURSOR", err.Error(), nil)
		return
	}
	var conversation communicationConversation
	if err := s.db.Raw(`SELECT conv_id, participant_a, participant_b, COALESCE(origin_type, '') AS origin_type, updated_at
		FROM conversations WHERE conv_id = ? AND status = 0 AND (participant_a = ? OR participant_b = ?)`,
		convID, viewerID, viewerID).Scan(&conversation).Error; err != nil || conversation.ConvID == 0 {
		fail(c, http.StatusNotFound, "CONVERSATION_NOT_FOUND", "conversation not found", nil)
		return
	}
	peerID := conversation.ParticipantA
	if peerID == viewerID {
		peerID = conversation.ParticipantB
	}
	query := s.db.Raw(`SELECT msg_id, conv_id, sender_id, receiver_id, content, is_read, created_at
		FROM private_messages WHERE conv_id = ? AND (? = 0 OR msg_id < ?)
		ORDER BY msg_id DESC LIMIT ?`, convID, cursor, cursor, limit+1)
	var messages []communicationMessage
	if err := query.Scan(&messages).Error; err != nil {
		fail(c, http.StatusInternalServerError, "MESSAGES_READ_FAILED", "could not read messages", nil)
		return
	}
	hasMore := len(messages) > limit
	if hasMore {
		messages = messages[:limit]
	}
	for index := range messages {
		boundCommunicationMessage(&messages[index], 56000)
	}
	relations, err := s.loadViewerRelations(viewerID, []int64{peerID})
	if err != nil {
		fail(c, http.StatusInternalServerError, "MESSAGES_READ_FAILED", "could not read relationship state", nil)
		return
	}
	contexts, err := s.loadCommunicationContexts(viewerID, []int64{peerID}, relations)
	if err != nil {
		fail(c, http.StatusInternalServerError, "IDENTITY_READ_FAILED", "could not resolve Agent identity", nil)
		return
	}
	nextCursor := ""
	if hasMore && len(messages) > 0 {
		nextCursor = strconv.FormatInt(messages[len(messages)-1].MsgID, 10)
	}
	data := map[string]interface{}{
		"viewer_agent_id": strconv.FormatInt(viewerID, 10), "conv_id": strconv.FormatInt(convID, 10),
		"messages": messages, "agent_contexts": contexts, "next_cursor": nextCursor, "has_more": hasMore,
	}
	for len(messages) > 1 && !communicationReplyFits(data) {
		messages = messages[:len(messages)-1]
		hasMore = true
		nextCursor = strconv.FormatInt(messages[len(messages)-1].MsgID, 10)
		data["messages"], data["next_cursor"], data["has_more"] = messages, nextCursor, hasMore
	}
	if !communicationReplyFits(data) {
		fail(c, http.StatusInternalServerError, "COMMUNICATION_RESPONSE_TOO_LARGE", "one message cannot fit the V2 response budget", nil)
		return
	}
	reply(c, http.StatusOK, data)
}

type communicationFriendRequest struct {
	RequestID         int64  `gorm:"column:id" json:"request_id,string"`
	Direction         string `json:"direction"`
	PeerAgentID       int64  `json:"peer_agent_id,string"`
	Greeting          string `gorm:"column:greeting" json:"greeting"`
	GreetingTruncated bool   `gorm:"-" json:"greeting_truncated,omitempty"`
	Remark            string `gorm:"column:remark" json:"remark,omitempty"`
	RemarkTruncated   bool   `gorm:"-" json:"remark_truncated,omitempty"`
	CreatedAt         int64  `gorm:"column:created_at" json:"created_at"`
	FromUID           int64  `gorm:"column:from_uid" json:"-"`
	ToUID             int64  `gorm:"column:to_uid" json:"-"`
}

func (s *Service) listCommunicationFriendRequests(_ context.Context, c *app.RequestContext) {
	viewerID, _ := agentID(c)
	direction := c.Query("direction")
	if direction != "incoming" && direction != "outgoing" {
		fail(c, http.StatusBadRequest, "INVALID_DIRECTION", "direction must be incoming or outgoing", nil)
		return
	}
	limit, err := parseCommunicationLimit(c)
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_LIMIT", err.Error(), nil)
		return
	}
	cursor, err := parseIDCursor(c.Query("cursor"))
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_CURSOR", err.Error(), nil)
		return
	}
	subjectColumn := "to_uid"
	remarkProjection := "'' AS remark"
	if direction == "outgoing" {
		subjectColumn = "from_uid"
		remarkProjection = "remark"
	}
	var requests []communicationFriendRequest
	query := `SELECT id, from_uid, to_uid, greeting, ` + remarkProjection + `, created_at FROM friend_requests
		WHERE status = 0 AND ` + subjectColumn + ` = ? AND (? = 0 OR id < ?)
		ORDER BY id DESC LIMIT ?`
	if err := s.db.Raw(query, viewerID, cursor, cursor, limit+1).Scan(&requests).Error; err != nil {
		fail(c, http.StatusInternalServerError, "FRIEND_REQUESTS_READ_FAILED", "could not read friend requests", nil)
		return
	}
	hasMore := len(requests) > limit
	if hasMore {
		requests = requests[:limit]
	}
	peerIDs := make([]int64, 0, len(requests))
	relations := make(map[int64]string, len(requests))
	for index := range requests {
		requests[index].Direction = direction
		requests[index].PeerAgentID = requests[index].FromUID
		if direction == "outgoing" {
			requests[index].PeerAgentID = requests[index].ToUID
		}
		requests[index].Greeting, requests[index].GreetingTruncated = truncateRunes(requests[index].Greeting, 2000)
		requests[index].Remark, requests[index].RemarkTruncated = truncateRunes(requests[index].Remark, 500)
		peerIDs = append(peerIDs, requests[index].PeerAgentID)
		relations[requests[index].PeerAgentID] = "pending"
	}
	contexts, err := s.loadCommunicationContexts(viewerID, peerIDs, relations)
	if err != nil {
		fail(c, http.StatusInternalServerError, "IDENTITY_READ_FAILED", "could not resolve Agent identities", nil)
		return
	}
	nextCursor := ""
	if hasMore && len(requests) > 0 {
		nextCursor = strconv.FormatInt(requests[len(requests)-1].RequestID, 10)
	}
	data := map[string]interface{}{
		"viewer_agent_id": strconv.FormatInt(viewerID, 10), "direction": direction,
		"friend_requests": requests, "agent_contexts": contexts, "next_cursor": nextCursor, "has_more": hasMore,
	}
	for len(requests) > 1 && !communicationReplyFits(data) {
		requests = requests[:len(requests)-1]
		hasMore = true
		peerIDs = peerIDs[:len(requests)]
		contexts = filterCommunicationContexts(contexts, peerIDs)
		nextCursor = strconv.FormatInt(requests[len(requests)-1].RequestID, 10)
		data["friend_requests"], data["agent_contexts"] = requests, contexts
		data["next_cursor"], data["has_more"] = nextCursor, hasMore
	}
	if !communicationReplyFits(data) {
		fail(c, http.StatusInternalServerError, "COMMUNICATION_RESPONSE_TOO_LARGE", "one friend request cannot fit the V2 response budget", nil)
		return
	}
	reply(c, http.StatusOK, data)
}

type communicationFriend struct {
	RelationID      int64                 `gorm:"column:id" json:"relation_id"`
	PeerAgentID     int64                 `gorm:"column:to_uid" json:"peer_agent_id,string"`
	Remark          string                `gorm:"column:remark" json:"remark"`
	RemarkTruncated bool                  `gorm:"-" json:"remark_truncated,omitempty"`
	FriendSince     int64                 `gorm:"column:created_at" json:"friend_since"`
	LastMessage     *communicationMessage `gorm:"-" json:"last_message,omitempty"`
}

func (s *Service) listCommunicationFriends(_ context.Context, c *app.RequestContext) {
	viewerID, _ := agentID(c)
	limit, err := parseCommunicationLimit(c)
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_LIMIT", err.Error(), nil)
		return
	}
	cursor, err := parseIDCursor(c.Query("cursor"))
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_CURSOR", err.Error(), nil)
		return
	}
	var friends []communicationFriend
	if err := s.db.Raw(`SELECT id, to_uid, remark, created_at FROM user_relations
		WHERE from_uid = ? AND rel_type = 1 AND (? = 0 OR id < ?)
		ORDER BY id DESC LIMIT ?`, viewerID, cursor, cursor, limit+1).Scan(&friends).Error; err != nil {
		fail(c, http.StatusInternalServerError, "FRIENDS_READ_FAILED", "could not read friends", nil)
		return
	}
	hasMore := len(friends) > limit
	if hasMore {
		friends = friends[:limit]
	}
	peerIDs := make([]int64, 0, len(friends))
	for index := range friends {
		friends[index].Remark, friends[index].RemarkTruncated = truncateRunes(friends[index].Remark, 500)
		peerIDs = append(peerIDs, friends[index].PeerAgentID)
	}
	if len(peerIDs) > 0 {
		type latestFriendMessage struct {
			PeerID int64 `gorm:"column:peer_id"`
			communicationMessage
		}
		var latest []latestFriendMessage
		if err := s.db.Raw(`SELECT c.peer_id, pm.msg_id, pm.conv_id, pm.sender_id, pm.receiver_id, pm.content, pm.is_read, pm.created_at
			FROM (
				SELECT conv_id, CASE WHEN participant_a = ? THEN participant_b ELSE participant_a END AS peer_id
				FROM conversations
				WHERE status = 0 AND origin_type = 'friend'
				  AND ((participant_a = ? AND participant_b = ANY(?)) OR (participant_b = ? AND participant_a = ANY(?)))
			) c
			JOIN LATERAL (
				SELECT msg_id, conv_id, sender_id, receiver_id, content, is_read, created_at
				FROM private_messages WHERE conv_id = c.conv_id ORDER BY msg_id DESC LIMIT 1
			) pm ON true`, viewerID, viewerID, pq.Array(peerIDs), viewerID, pq.Array(peerIDs)).Scan(&latest).Error; err != nil {
			fail(c, http.StatusInternalServerError, "FRIENDS_READ_FAILED", "could not read recent friend messages", nil)
			return
		}
		lastByPeer := make(map[int64]*communicationMessage, len(latest))
		for index := range latest {
			message := latest[index].communicationMessage
			boundCommunicationMessage(&message, 1000)
			lastByPeer[latest[index].PeerID] = &message
		}
		for index := range friends {
			friends[index].LastMessage = lastByPeer[friends[index].PeerAgentID]
		}
	}
	relations := make(map[int64]string, len(peerIDs))
	for _, peerID := range peerIDs {
		relations[peerID] = "friend"
	}
	contexts, err := s.loadCommunicationContexts(viewerID, peerIDs, relations)
	if err != nil {
		fail(c, http.StatusInternalServerError, "IDENTITY_READ_FAILED", "could not resolve Agent identities", nil)
		return
	}
	var total int64
	if err := s.db.Raw(`SELECT COUNT(*) FROM user_relations WHERE from_uid = ? AND rel_type = 1`, viewerID).Scan(&total).Error; err != nil {
		fail(c, http.StatusInternalServerError, "FRIENDS_READ_FAILED", "could not count friends", nil)
		return
	}
	nextCursor := ""
	if hasMore && len(friends) > 0 {
		nextCursor = strconv.FormatInt(friends[len(friends)-1].RelationID, 10)
	}
	data := map[string]interface{}{
		"viewer_agent_id": strconv.FormatInt(viewerID, 10), "friends": friends,
		"agent_contexts": contexts, "next_cursor": nextCursor, "has_more": hasMore, "total": total,
	}
	for len(friends) > 1 && !communicationReplyFits(data) {
		friends = friends[:len(friends)-1]
		hasMore = true
		peerIDs = peerIDs[:len(friends)]
		contexts = filterCommunicationContexts(contexts, peerIDs)
		nextCursor = strconv.FormatInt(friends[len(friends)-1].RelationID, 10)
		data["friends"], data["agent_contexts"] = friends, contexts
		data["next_cursor"], data["has_more"] = nextCursor, hasMore
	}
	if !communicationReplyFits(data) {
		fail(c, http.StatusInternalServerError, "COMMUNICATION_RESPONSE_TOO_LARGE", "one friend record cannot fit the V2 response budget", nil)
		return
	}
	reply(c, http.StatusOK, data)
}
