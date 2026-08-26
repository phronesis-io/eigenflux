package consolev2

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/lib/pq"

	"eigenflux_server/pkg/agentcard"
)

const (
	todayEncounterLimit     = 8
	todayParticipationLimit = 5
	todayFocusLimit         = 8
	todayRuntimeFreshness   = 30 * time.Minute
)

type todayEncounter struct {
	PeerAgentID      int64  `gorm:"column:peer_agent_id" json:"peer_agent_id,string"`
	LastInteraction  int64  `gorm:"column:last_interaction_at" json:"last_interaction_at"`
	InteractionCount int64  `gorm:"column:interaction_count" json:"interaction_count"`
	CountryCode      string `gorm:"column:country_code" json:"country_code,omitempty"`
	TotalCount       int64  `gorm:"column:total_count" json:"-"`
}

func todayCountryCode(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	if code, ok := onboardingCountryAliases[strings.ToUpper(value)]; ok {
		if code == "ZZ" {
			return ""
		}
		return code
	}
	upper := strings.ToUpper(value)
	if countryCodePattern.MatchString(upper) && upper != "ZZ" {
		return upper
	}
	return ""
}

var consoleV2CardCompletionFields = []string{
	"agent_name", "agent_description", "human_description", "working_languages",
	"seeking", "offering", "geo", "timezone", "agent_status", "human_status", "interests_negative",
}

type todayCommandReceipt struct {
	AttentionID int64  `gorm:"column:attention_id"`
	CommandID   int64  `gorm:"column:command_id"`
	Status      string `gorm:"column:status"`
	CreatedAt   int64  `gorm:"column:created_at"`
	CompletedAt *int64 `gorm:"column:completed_at"`
	Result      string `gorm:"column:result"`
}

type todayAttentionSource struct {
	SourceType    string `gorm:"column:source_type"`
	SourceID      int64  `gorm:"column:source_id"`
	AuthorAgentID int64  `gorm:"column:author_agent_id"`
}

type todayObservationFacts struct {
	RuntimeKnown         bool  `gorm:"column:runtime_known"`
	Connected            bool  `gorm:"column:connected"`
	LastHeartbeatAt      int64 `gorm:"column:last_heartbeat_at"`
	FirstScanCompletedAt int64 `gorm:"column:first_scan_completed_at"`
	LastScanAt           int64 `gorm:"column:last_scan_at"`
	ActivityCount        int64 `gorm:"column:activity_count"`
	HeatActivityCount    int64 `gorm:"column:heat_activity_count"`
}

func todayEmptyModuleState(hasData, firstScanCompleted, connected, runtimeKnown bool) string {
	if hasData {
		return "data"
	}
	if runtimeKnown && !connected {
		return "offline"
	}
	if firstScanCompleted {
		return "complete_empty"
	}
	if connected {
		return "starting"
	}
	return "waiting"
}

func todayObservationState(hasResult, firstScanCompleted, connected, runtimeKnown bool) string {
	return todayEmptyModuleState(hasResult, firstScanCompleted, connected, runtimeKnown)
}

func uniqueInt64s(values []int64) []int64 {
	seen := make(map[int64]struct{}, len(values))
	result := make([]int64, 0, len(values))
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func cardFieldPresent(value interface{}) bool {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed) != ""
	case []interface{}:
		for _, item := range typed {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				return true
			}
		}
		return false
	case map[string]interface{}:
		return len(typed) > 0
	default:
		return value != nil
	}
}

func calculateCardCompletionValues(values map[string]interface{}) (int, int, int) {
	completed := 0
	for _, field := range consoleV2CardCompletionFields {
		if cardFieldPresent(values[field]) {
			completed++
		}
	}
	total := len(consoleV2CardCompletionFields)
	percent := 0
	if total > 0 {
		percent = (completed*100 + total/2) / total
	}
	return completed, total, percent
}

func (s *Service) todayCapabilities() map[string]interface{} {
	return map[string]interface{}{
		"control_enabled":            s.enableControl,
		"attention_enabled":          s.enableAttentionV1,
		"agent_attention_v1_enabled": s.enableAttentionV1,
	}
}

func calculateCardCompletion(publicJSON, privateJSON string) (int, int, int, error) {
	publicCard := map[string]interface{}{}
	privateCard := map[string]interface{}{}
	if strings.TrimSpace(publicJSON) != "" {
		if err := json.Unmarshal([]byte(publicJSON), &publicCard); err != nil {
			return 0, 0, 0, err
		}
	}
	if strings.TrimSpace(privateJSON) != "" {
		if err := json.Unmarshal([]byte(privateJSON), &privateCard); err != nil {
			return 0, 0, 0, err
		}
	}
	values := map[string]interface{}{}
	for _, field := range consoleV2CardCompletionFields {
		source := privateCard
		for _, spec := range agentcard.EditableFields {
			if spec.Name == field && spec.Public {
				source = publicCard
				break
			}
		}
		values[field] = source[field]
	}
	completed, total, percent := calculateCardCompletionValues(values)
	return completed, total, percent, nil
}

func calculateCurrentCardCompletion(agentName, agentDescription, profileJSON string) (int, int, int, error) {
	values := map[string]interface{}{}
	if strings.TrimSpace(profileJSON) != "" {
		if err := json.Unmarshal([]byte(profileJSON), &values); err != nil {
			return 0, 0, 0, err
		}
	}
	values["agent_name"] = agentName
	values["agent_description"] = agentDescription
	completed, total, percent := calculateCardCompletionValues(values)
	return completed, total, percent, nil
}

func todayLocationFromPrivateCard(privateJSON string) (*time.Location, string) {
	location := time.UTC
	name := "UTC"
	var privateCard map[string]interface{}
	if json.Unmarshal([]byte(privateJSON), &privateCard) == nil {
		if timezone, ok := privateCard["timezone"].(string); ok {
			candidate := strings.TrimSpace(strings.SplitN(timezone, "(", 2)[0])
			if loaded, err := time.LoadLocation(candidate); err == nil {
				location = loaded
				name = candidate
			}
		}
	}
	return location, name
}

func todayStartFromPrivateCard(privateJSON string, now time.Time) int64 {
	location, _ := todayLocationFromPrivateCard(privateJSON)
	localNow := now.In(location)
	return time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, location).UTC().UnixMilli()
}

type todayAttentionPage struct {
	Rows       []attentionView
	NextCursor string
	HasMore    bool
}

func (s *Service) loadTodayAttentions(agentID, since int64, participationCursor, focusCursor attentionCursor) (todayAttentionPage, todayAttentionPage, int64, int64, error) {
	var counts struct {
		Focus         int64 `gorm:"column:focus_count"`
		Participation int64 `gorm:"column:participation_count"`
	}
	if err := s.db.Raw(`SELECT COUNT(*) FILTER (WHERE surface = 'focus') AS focus_count,
		COUNT(*) FILTER (WHERE surface = 'participation') AS participation_count
		FROM agent_attention_items WHERE agent_id = ? AND producer = 'agent'
		  AND protocol_version = 'agent_attention.v1'
		  AND status IN ('open','selected','pending','acted') AND created_at >= ?
		  AND expires_at > (extract(epoch FROM clock_timestamp())*1000)::bigint`, agentID, since).Scan(&counts).Error; err != nil {
		return todayAttentionPage{}, todayAttentionPage{}, 0, 0, err
	}

	load := func(participation bool, limit int, cursor attentionCursor) (todayAttentionPage, error) {
		var rows []attentionView
		query := attentionSelect + ` WHERE item.agent_id = ? AND item.producer = 'agent'
			AND item.protocol_version = 'agent_attention.v1'
			AND item.created_at >= ? AND item.status IN ('open','selected','pending','acted')
			AND item.expires_at > (extract(epoch FROM clock_timestamp())*1000)::bigint`
		args := []interface{}{agentID, since}
		if participation {
			query += ` AND item.surface = 'participation'`
		} else {
			query += ` AND item.surface = 'focus'`
		}
		if cursor.AttentionID > 0 {
			query += ` AND (item.created_at, item.attention_id) < (?, ?)`
			args = append(args, cursor.CreatedAt, cursor.AttentionID)
		}
		query += ` ORDER BY item.created_at DESC, item.attention_id DESC LIMIT ?`
		args = append(args, limit+1)
		if err := s.db.Raw(query, args...).Scan(&rows).Error; err != nil {
			return todayAttentionPage{}, err
		}
		page := todayAttentionPage{Rows: rows, HasMore: len(rows) > limit}
		if page.HasMore {
			page.Rows = rows[:limit]
			last := page.Rows[len(page.Rows)-1]
			page.NextCursor = encodeAttentionCursor(attentionCursor{CreatedAt: last.CreatedAt, AttentionID: last.AttentionID})
		}
		return page, nil
	}
	participation, err := load(true, todayParticipationLimit, participationCursor)
	if err != nil {
		return todayAttentionPage{}, todayAttentionPage{}, 0, 0, err
	}
	focus, err := load(false, todayFocusLimit, focusCursor)
	if err != nil {
		return todayAttentionPage{}, todayAttentionPage{}, 0, 0, err
	}
	return participation, focus, counts.Focus, counts.Participation, nil
}

func (s *Service) loadTodayCommandReceipts(agentID int64, attentionIDs []int64) (map[int64]map[string]interface{}, error) {
	result := make(map[int64]map[string]interface{}, len(attentionIDs))
	if len(attentionIDs) == 0 {
		return result, nil
	}
	var rows []todayCommandReceipt
	if err := s.db.Raw(`SELECT DISTINCT ON (attention_id) attention_id, command_id, status,
		created_at, completed_at, COALESCE(result, '{}'::jsonb)::text AS result
		FROM agent_commands
		WHERE agent_id = ? AND attention_id = ANY(?) AND command_type = 'attention_response'
		  AND payload->>'protocol_version' = 'agent_attention.v1'
		ORDER BY attention_id, created_at DESC, command_id DESC`, agentID, pq.Array(attentionIDs)).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		var receiptResult interface{}
		if json.Unmarshal([]byte(row.Result), &receiptResult) != nil {
			receiptResult = map[string]interface{}{}
		}
		result[row.AttentionID] = map[string]interface{}{
			"command_id": fmt.Sprintf("%d", row.CommandID), "status": row.Status,
			"created_at": row.CreatedAt, "completed_at": row.CompletedAt, "result": receiptResult,
		}
	}
	return result, nil
}

func (s *Service) loadTodayAttentionSources(agentID int64, rows ...[]attentionView) (map[string]int64, error) {
	requested := make([]map[string]interface{}, 0, todayParticipationLimit+todayFocusLimit)
	seen := make(map[string]struct{}, cap(requested))
	for _, group := range rows {
		for _, row := range group {
			key := fmt.Sprintf("%s:%d", row.SourceType, row.SourceID)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			requested = append(requested, map[string]interface{}{"source_type": row.SourceType, "source_id": row.SourceID})
		}
	}
	result := make(map[string]int64, len(requested))
	if len(requested) == 0 {
		return result, nil
	}
	encoded, err := json.Marshal(requested)
	if err != nil {
		return nil, err
	}
	var sources []todayAttentionSource
	if err := s.db.Raw(`WITH requested AS (
			SELECT * FROM jsonb_to_recordset(?::jsonb) AS row(source_type text, source_id bigint)
		), resolved AS (
		SELECT exposure.source_type, exposure.source_id, exposure.author_agent_id
		FROM requested
		JOIN agent_feed_exposures exposure ON exposure.agent_id = ?
		 AND exposure.source_type = requested.source_type AND exposure.source_id = requested.source_id
		WHERE exposure.author_agent_id IS NOT NULL
		UNION ALL
		SELECT requested.source_type, requested.source_id,
			CASE WHEN message.sender_id = ? THEN message.receiver_id ELSE message.sender_id END
		FROM requested JOIN private_messages message ON message.msg_id = requested.source_id
		WHERE requested.source_type = 'private_message'
		  AND (message.sender_id = ? OR message.receiver_id = ?)
		UNION ALL
		SELECT requested.source_type, requested.source_id,
			CASE WHEN message.sender_id = ? THEN message.receiver_id ELSE message.sender_id END
		FROM requested
		JOIN private_messages message ON message.msg_id = requested.source_id
		JOIN conversations conversation ON conversation.conv_id = message.conv_id
		JOIN agent_feed_exposures exposure ON exposure.agent_id = ?
		 AND exposure.source_type = 'broadcast' AND exposure.source_id = conversation.origin_id
		WHERE requested.source_type = 'broadcast_reply'
		  AND conversation.origin_type = 'broadcast'
		  AND (message.sender_id = ? OR message.receiver_id = ?)
		UNION ALL
		SELECT requested.source_type, requested.source_id,
			CASE WHEN request.from_uid = ? THEN request.to_uid ELSE request.from_uid END
		FROM requested JOIN friend_requests request ON request.id = requested.source_id
		WHERE requested.source_type = 'friend_request'
		  AND (request.from_uid = ? OR request.to_uid = ?)
		UNION ALL
		SELECT requested.source_type, requested.source_id,
			CASE WHEN relation.from_uid = ? THEN relation.to_uid ELSE relation.from_uid END
		FROM requested JOIN user_relations relation ON relation.id = requested.source_id
		WHERE requested.source_type = 'relation'
		  AND (relation.from_uid = ? OR relation.to_uid = ?)
		)
		SELECT source_type, source_id, author_agent_id FROM resolved WHERE author_agent_id IS NOT NULL`,
		string(encoded),
		agentID,
		agentID, agentID, agentID,
		agentID, agentID, agentID, agentID,
		agentID, agentID, agentID,
		agentID, agentID, agentID).Scan(&sources).Error; err != nil {
		return nil, err
	}
	for _, source := range sources {
		result[fmt.Sprintf("%s:%d", source.SourceType, source.SourceID)] = source.AuthorAgentID
	}
	return result, nil
}

func (s *Service) getToday(_ context.Context, c *app.RequestContext) {
	agentIDValue, _ := agentID(c)
	now := time.Now().UTC()
	participationCursor, err := decodeAttentionCursor(c.Query("participation_cursor"))
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_CURSOR", "participation cursor is invalid", nil)
		return
	}
	focusCursor, err := decodeAttentionCursor(c.Query("focus_cursor"))
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_CURSOR", "focus cursor is invalid", nil)
		return
	}

	var goal struct {
		GoalID   int64  `gorm:"column:goal_id"`
		GoalText string `gorm:"column:goal_text"`
	}
	if err := s.db.Raw(`SELECT goal_id, goal_text FROM agent_network_goals
		WHERE agent_id = ? AND status = 'active' ORDER BY updated_at DESC LIMIT 1`, agentIDValue).Scan(&goal).Error; err != nil {
		fail(c, http.StatusInternalServerError, "TODAY_READ_FAILED", "could not load network goal", nil)
		return
	}

	var card struct {
		PublicCard       string `gorm:"column:public_card"`
		PrivateCard      string `gorm:"column:private_card"`
		AgentName        string `gorm:"column:agent_name"`
		AgentDescription string `gorm:"column:agent_description"`
		ProfileData      string `gorm:"column:profile_data"`
	}
	if err := s.db.Raw(`SELECT COALESCE(card.public_card, '{}'::jsonb)::text AS public_card,
			COALESCE(card.private_card, '{}'::jsonb)::text AS private_card,
			agent.agent_name, agent.bio AS agent_description,
			COALESCE(profile.profile_data, '{}'::jsonb)::text AS profile_data
		FROM agents agent
		LEFT JOIN agent_cards card ON card.agent_id = agent.agent_id
		LEFT JOIN agent_profiles profile ON profile.agent_id = agent.agent_id
		WHERE agent.agent_id = ?`, agentIDValue).Scan(&card).Error; err != nil {
		fail(c, http.StatusInternalServerError, "TODAY_READ_FAILED", "could not load Agent Card", nil)
		return
	}
	completedFields, totalFields, completionPercent, err := calculateCurrentCardCompletion(card.AgentName, card.AgentDescription, card.ProfileData)
	if err != nil {
		fail(c, http.StatusInternalServerError, "TODAY_READ_FAILED", "could not calculate Agent Card completion", nil)
		return
	}
	location, timezoneName := todayLocationFromPrivateCard(card.PrivateCard)
	todayStart := todayStartFromPrivateCard(card.PrivateCard, now)
	todayDay := now.In(location).Format("2006-01-02")

	var encounters []todayEncounter
	if err := s.db.Raw(`WITH recent AS (
			SELECT participant_b AS peer_agent_id, updated_at, conv_id
			FROM conversations WHERE participant_a = ? AND updated_at >= ?
			UNION ALL
			SELECT participant_a AS peer_agent_id, updated_at, conv_id
			FROM conversations WHERE participant_b = ? AND updated_at >= ?
			UNION ALL
			SELECT exposure.author_agent_id AS peer_agent_id,
				exposure.last_seen_at AS updated_at, exposure.exposure_id AS conv_id
			FROM agent_feed_exposures exposure
			WHERE exposure.agent_id = ? AND exposure.last_seen_at >= ?
			  AND exposure.author_agent_id IS NOT NULL
		), grouped AS (
			SELECT peer_agent_id, MAX(updated_at) AS last_interaction_at,
				COUNT(*)::bigint AS interaction_count
			FROM recent WHERE peer_agent_id <> ? GROUP BY peer_agent_id
		)
		SELECT grouped.peer_agent_id, grouped.last_interaction_at, grouped.interaction_count,
			COALESCE(NULLIF(BTRIM(profile.profile_data->>'geo'), ''),
				NULLIF(BTRIM(profile.country), '')) AS country_code,
			COUNT(*) OVER() AS total_count
		FROM grouped
		LEFT JOIN agent_profiles profile ON profile.agent_id = grouped.peer_agent_id
		ORDER BY grouped.last_interaction_at DESC, grouped.peer_agent_id DESC LIMIT ?`,
		agentIDValue, todayStart, agentIDValue, todayStart,
		agentIDValue, todayStart,
		agentIDValue, todayEncounterLimit).Scan(&encounters).Error; err != nil {
		fail(c, http.StatusInternalServerError, "TODAY_READ_FAILED", "could not load today's Agent encounters", nil)
		return
	}
	peerIDs := make([]int64, 0, len(encounters))
	encounterCount := int64(0)
	if len(encounters) > 0 {
		encounterCount = encounters[0].TotalCount
	}
	for index := range encounters {
		encounters[index].CountryCode = todayCountryCode(encounters[index].CountryCode)
		peerIDs = append(peerIDs, encounters[index].PeerAgentID)
	}
	attentionSince := now.Add(-24 * time.Hour).UnixMilli()
	var participationPage, focusPage todayAttentionPage
	var focusCount, participationCount int64
	participationItems := make([]map[string]interface{}, 0)
	focusItems := make([]map[string]interface{}, 0)
	if s.enableAttentionV1 {
		participationPage, focusPage, focusCount, participationCount, err = s.loadTodayAttentions(
			agentIDValue, attentionSince, participationCursor, focusCursor)
		if err != nil {
			fail(c, http.StatusInternalServerError, "TODAY_READ_FAILED", "could not load Attention items", nil)
			return
		}
		participation := participationPage.Rows
		focus := focusPage.Rows
		attentionIDs := make([]int64, 0, len(participation)+len(focus))
		for _, item := range participation {
			attentionIDs = append(attentionIDs, item.AttentionID)
		}
		for _, item := range focus {
			attentionIDs = append(attentionIDs, item.AttentionID)
		}
		receipts, receiptErr := s.loadTodayCommandReceipts(agentIDValue, attentionIDs)
		if receiptErr != nil {
			fail(c, http.StatusInternalServerError, "TODAY_READ_FAILED", "could not load participation receipts", nil)
			return
		}
		participationItems = make([]map[string]interface{}, 0, len(participation))
		for _, item := range participation {
			view := attentionResponse(item)
			if receipt, exists := receipts[item.AttentionID]; exists {
				view["latest_command"] = receipt
			}
			participationItems = append(participationItems, view)
		}
		focusItems = make([]map[string]interface{}, 0, len(focus))
		for _, item := range focus {
			view := attentionResponse(item)
			if receipt, exists := receipts[item.AttentionID]; exists {
				view["latest_command"] = receipt
			}
			focusItems = append(focusItems, view)
		}
		sourceAgents, sourceErr := s.loadTodayAttentionSources(agentIDValue, participation, focus)
		if sourceErr != nil {
			fail(c, http.StatusInternalServerError, "TODAY_READ_FAILED", "could not load Attention sources", nil)
			return
		}
		for index, item := range participation {
			if sourceID := sourceAgents[fmt.Sprintf("%s:%d", item.SourceType, item.SourceID)]; sourceID > 0 {
				participationItems[index]["source_agent_id"] = strconv.FormatInt(sourceID, 10)
				peerIDs = append(peerIDs, sourceID)
			}
		}
		for index, item := range focus {
			if sourceID := sourceAgents[fmt.Sprintf("%s:%d", item.SourceType, item.SourceID)]; sourceID > 0 {
				focusItems[index]["source_agent_id"] = strconv.FormatInt(sourceID, 10)
				peerIDs = append(peerIDs, sourceID)
			}
		}
	}
	peerIDs = uniqueInt64s(peerIDs)
	relations, err := s.loadViewerRelations(agentIDValue, peerIDs)
	if err != nil {
		fail(c, http.StatusInternalServerError, "TODAY_READ_FAILED", "could not load Agent relations", nil)
		return
	}
	contexts, err := s.loadCommunicationContexts(agentIDValue, peerIDs, relations)
	if err != nil {
		fail(c, http.StatusInternalServerError, "TODAY_READ_FAILED", "could not load Agent summaries", nil)
		return
	}

	var observation todayObservationFacts
	if err := s.db.Raw(`WITH clock AS (
			SELECT (extract(epoch FROM clock_timestamp())*1000)::bigint AS now_ms
		), runtime AS (
			SELECT COUNT(*)::bigint AS runtime_count,
				COALESCE(MAX(last_heartbeat_at), 0)::bigint AS last_heartbeat_at
			FROM agent_runtime_leases WHERE agent_id = ?
		), activity AS (
			SELECT COUNT(*)::bigint AS activity_count,
				COUNT(*)::bigint AS heat_activity_count,
				COALESCE(MIN(created_at) FILTER (WHERE event_type = 'feed_pull'), 0)::bigint AS first_scan_completed_at,
				COALESCE(MAX(created_at) FILTER (WHERE event_type = 'feed_pull'), 0)::bigint AS last_scan_at
			FROM agent_activity_log WHERE agent_id = ? AND created_at >= ?
		)
		SELECT runtime.runtime_count > 0 AS runtime_known,
			runtime.last_heartbeat_at >= clock.now_ms - ? AS connected,
			runtime.last_heartbeat_at, activity.first_scan_completed_at,
			activity.last_scan_at, activity.activity_count, activity.heat_activity_count
		FROM clock CROSS JOIN runtime CROSS JOIN activity`, agentIDValue, agentIDValue, todayStart,
		int64(todayRuntimeFreshness/time.Millisecond)).Scan(&observation).Error; err != nil {
		fail(c, http.StatusInternalServerError, "TODAY_READ_FAILED", "could not load today's observation state", nil)
		return
	}
	firstScanCompleted := observation.FirstScanCompletedAt > 0
	hasBriefResult := encounterCount > 0 || participationCount > 0 || focusCount > 0
	moduleStates := map[string]string{
		"heat":          todayEmptyModuleState(observation.HeatActivityCount > 0, firstScanCompleted, observation.Connected, observation.RuntimeKnown),
		"encounters":    todayEmptyModuleState(encounterCount > 0, firstScanCompleted, observation.Connected, observation.RuntimeKnown),
		"participation": todayEmptyModuleState(participationCount > 0, firstScanCompleted, observation.Connected, observation.RuntimeKnown),
		"focus":         todayEmptyModuleState(focusCount > 0, firstScanCompleted, observation.Connected, observation.RuntimeKnown),
		"activity":      todayEmptyModuleState(observation.ActivityCount > 0, firstScanCompleted, observation.Connected, observation.RuntimeKnown),
	}
	firstScanState := "not_started"
	if firstScanCompleted {
		firstScanState = "completed"
	} else if observation.Connected {
		firstScanState = "running"
	} else if observation.RuntimeKnown {
		firstScanState = "offline"
	}

	goalData := interface{}(nil)
	if goal.GoalID > 0 {
		goalData = map[string]interface{}{"goal_id": strconv.FormatInt(goal.GoalID, 10), "goal_text": goal.GoalText}
	}
	reply(c, http.StatusOK, map[string]interface{}{
		"schema_version": "console_today.v2",
		"generated_at":   now.UnixMilli(),
		"day":            todayDay, "timezone": timezoneName, "window_start": todayStart,
		"capabilities": s.todayCapabilities(),
		"network_goal": goalData,
		"card_completion": map[string]interface{}{
			"completed_fields": completedFields, "total_fields": totalFields, "percent": completionPercent,
		},
		"brief": map[string]interface{}{
			"focus_count": focusCount, "participation_count": participationCount,
			"encounter_count": encounterCount, "activity_count": observation.ActivityCount,
		},
		"observation": map[string]interface{}{
			"state":                   todayObservationState(hasBriefResult, firstScanCompleted, observation.Connected, observation.RuntimeKnown),
			"connected":               observation.Connected,
			"runtime_known":           observation.RuntimeKnown,
			"first_scan_state":        firstScanState,
			"first_scan_completed_at": observation.FirstScanCompletedAt,
			"last_scan_at":            observation.LastScanAt,
			"last_heartbeat_at":       observation.LastHeartbeatAt,
		},
		"module_states": moduleStates,
		"encounters":    encounters, "agent_contexts": contexts,
		"participation_items": participationItems, "focus_items": focusItems,
		"attention_pagination": map[string]interface{}{
			"participation": map[string]interface{}{"next_cursor": participationPage.NextCursor, "has_more": participationPage.HasMore},
			"focus":         map[string]interface{}{"next_cursor": focusPage.NextCursor, "has_more": focusPage.HasMore},
		},
	})
}
