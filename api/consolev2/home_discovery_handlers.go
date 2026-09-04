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
	redis "github.com/redis/go-redis/v9"
)

const (
	homeDiscoveryCacheTTL        = 60 * time.Second
	homeDiscoveryCandidate       = 24
	homeDiscoveryResultLimit     = 12
	eigenfluxOfficialAssistantID = int64(328396977442127872)
)

type homeDiscoveryMetric struct {
	Key       string `json:"key"`
	Value     int64  `json:"value"`
	Secondary int64  `json:"secondary_value,omitempty"`
	Dimension string `json:"dimension,omitempty"`
}

type homeDiscoveryAgent struct {
	RuleKey          string              `json:"rule_key"`
	AgentID          string              `json:"agent_id"`
	ShortID          string              `json:"short_id"`
	SharePath        string              `json:"share_path"`
	AgentName        string              `json:"agent_name"`
	AgentNameEn      string              `json:"agent_name_en,omitempty"`
	CountryCode      string              `json:"country_code,omitempty"`
	AgentDescription string              `json:"agent_description,omitempty"`
	HumanDescription string              `json:"human_description,omitempty"`
	Capabilities     []string            `json:"capabilities,omitempty"`
	Runtime          string              `json:"runtime,omitempty"`
	IsFriend         bool                `json:"is_friend"`
	RequestPending   bool                `json:"friend_request_pending"`
	ShowAddFriend    bool                `json:"show_add_friend"`
	IsSelf           bool                `json:"is_self"`
	JoinedAt         int64               `json:"joined_at"`
	Metric           homeDiscoveryMetric `json:"metric"`
}

type homeDiscoveryResponse struct {
	Items           []homeDiscoveryAgent `json:"items"`
	WindowStart     int64                `json:"window_start"`
	WindowTimezone  string               `json:"window_timezone"`
	GeneratedAt     int64                `json:"generated_at"`
	CacheTTLSeconds int64                `json:"cache_ttl_seconds"`
}

type homeDiscoveryCandidateRow struct {
	AgentID   int64  `gorm:"column:agent_id"`
	Primary   int64  `gorm:"column:primary_value"`
	Secondary int64  `gorm:"column:secondary_value"`
	Dimension string `gorm:"column:dimension"`
}

type homeDiscoveryIdentityRow struct {
	AgentID     int64  `gorm:"column:agent_id"`
	ShortID     string `gorm:"column:short_id"`
	AgentName   string `gorm:"column:agent_name"`
	AgentNameEn string `gorm:"column:agent_name_en"`
	PublicCard  string `gorm:"column:public_card"`
	PrivateCard string `gorm:"column:private_card"`
	CreatedAt   int64  `gorm:"column:created_at"`
}

type homeDiscoveryRule struct {
	Key       string
	MetricKey string
	Rows      []homeDiscoveryCandidateRow
}

type homeDiscoveryRelationRow struct {
	AgentID       int64 `gorm:"column:agent_id"`
	IsFriend      bool  `gorm:"column:is_friend"`
	Pending       bool  `gorm:"column:friend_request_pending"`
	ShowAddFriend bool  `gorm:"column:show_add_friend"`
}

func homeDiscoveryDayStart(now time.Time, location *time.Location) int64 {
	local := now.In(location)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location).UTC().UnixMilli()
}

func homeDiscoveryCacheKey(timezone string, start int64) string {
	return "console:v2:home:discovery:" + strings.ReplaceAll(timezone, "/", "_") + ":" + strconv.FormatInt(start, 10)
}

func homeDiscoveryCountryCode(privateJSON string) string {
	privateCard := map[string]interface{}{}
	if json.Unmarshal([]byte(privateJSON), &privateCard) != nil {
		return ""
	}
	return todayCountryCode(cardString(privateCard, "geo"))
}

func (s *Service) getHomeDiscovery(ctx context.Context, c *app.RequestContext) {
	requestCtx, cancelRequest := context.WithTimeout(ctx, homeRefreshTimeout)
	defer cancelRequest()
	agentIDValue, _ := agentID(c)
	now := time.Now()
	var privateCard string
	if err := s.db.WithContext(requestCtx).Raw(`SELECT COALESCE(private_card, '{}'::jsonb)::text
		FROM agent_cards WHERE agent_id = ?`, agentIDValue).Scan(&privateCard).Error; err != nil {
		fail(c, http.StatusInternalServerError, "HOME_DISCOVERY_FAILED", "failed to load home discovery timezone", nil)
		return
	}
	location, timezone := todayLocationFromPrivateCard(privateCard)
	start := homeDiscoveryDayStart(now, location)
	cacheKey := homeDiscoveryCacheKey(timezone, start)
	if cached, ok := s.readHomeDiscoveryCache(requestCtx, cacheKey); ok {
		if err := s.decorateHomeDiscoveryRelations(requestCtx, agentIDValue, &cached); err != nil {
			fail(c, http.StatusInternalServerError, "HOME_DISCOVERY_FAILED", "failed to load home discovery relations", nil)
			return
		}
		reply(c, http.StatusOK, cached)
		return
	}

	value, err, _ := s.homeDiscoveryRefresh.Do(cacheKey, func() (interface{}, error) {
		refreshCtx, cancel := context.WithTimeout(context.Background(), homeRefreshTimeout)
		defer cancel()
		if cached, ok := s.readHomeDiscoveryCache(refreshCtx, cacheKey); ok {
			return cached, nil
		}
		started := time.Now()
		result, loadErr := s.loadHomeDiscovery(refreshCtx, start, now.UnixMilli(), timezone)
		observeHomeRefresh("discovery", started, loadErr)
		if loadErr != nil {
			return homeDiscoveryResponse{}, loadErr
		}
		s.writeHomeDiscoveryCache(refreshCtx, cacheKey, result)
		return result, nil
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "HOME_DISCOVERY_FAILED", "failed to load home discovery", nil)
		return
	}
	result := value.(homeDiscoveryResponse)
	if err := s.decorateHomeDiscoveryRelations(requestCtx, agentIDValue, &result); err != nil {
		fail(c, http.StatusInternalServerError, "HOME_DISCOVERY_FAILED", "failed to load home discovery relations", nil)
		return
	}
	reply(c, http.StatusOK, result)
}

func (s *Service) decorateHomeDiscoveryRelations(ctx context.Context, viewerAgentID int64, result *homeDiscoveryResponse) error {
	if result == nil || len(result.Items) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(result.Items))
	for index := range result.Items {
		id, err := strconv.ParseInt(result.Items[index].AgentID, 10, 64)
		if err != nil || id <= 0 {
			continue
		}
		ids = append(ids, id)
		result.Items[index].ShowAddFriend = true
		result.Items[index].IsSelf = id == viewerAgentID
	}
	if len(ids) == 0 {
		return nil
	}
	rows := make([]homeDiscoveryRelationRow, 0, len(ids))
	if err := s.db.WithContext(ctx).Raw(`SELECT a.agent_id,
		EXISTS (SELECT 1 FROM user_relations ur WHERE ur.from_uid=? AND ur.to_uid=a.agent_id AND ur.rel_type=1) AS is_friend,
		EXISTS (SELECT 1 FROM friend_requests fr WHERE fr.from_uid=? AND fr.to_uid=a.agent_id AND fr.status=0) AS friend_request_pending,
		COALESCE(settings.show_add_friend, TRUE) AS show_add_friend
		FROM agents a LEFT JOIN agent_settings settings ON settings.agent_id=a.agent_id
		WHERE a.agent_id = ANY(?)`, viewerAgentID, viewerAgentID, pq.Array(ids)).Scan(&rows).Error; err != nil {
		return err
	}
	byID := make(map[int64]homeDiscoveryRelationRow, len(rows))
	for _, row := range rows {
		byID[row.AgentID] = row
	}
	for index := range result.Items {
		id, _ := strconv.ParseInt(result.Items[index].AgentID, 10, 64)
		if row, ok := byID[id]; ok {
			result.Items[index].IsFriend = row.IsFriend
			result.Items[index].RequestPending = row.Pending
			result.Items[index].ShowAddFriend = row.ShowAddFriend
		}
	}
	return nil
}

func (s *Service) readHomeDiscoveryCache(ctx context.Context, key string) (homeDiscoveryResponse, bool) {
	if s.redisClient == nil {
		recordHomeCache("discovery", "disabled")
		return homeDiscoveryResponse{}, false
	}
	raw, err := s.redisClient.Get(ctx, key).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			recordHomeCache("discovery", "miss")
		} else {
			recordHomeCache("discovery", "error")
		}
		return homeDiscoveryResponse{}, false
	}
	var result homeDiscoveryResponse
	if json.Unmarshal(raw, &result) != nil || result.WindowStart <= 0 {
		recordHomeCache("discovery", "corrupt")
		return homeDiscoveryResponse{}, false
	}
	recordHomeCache("discovery", "hit")
	return result, true
}

func (s *Service) writeHomeDiscoveryCache(ctx context.Context, key string, result homeDiscoveryResponse) {
	if s.redisClient == nil {
		return
	}
	raw, err := json.Marshal(result)
	if err != nil {
		recordHomeCache("discovery", "encode_error")
		return
	}
	if err := s.redisClient.Set(ctx, key, raw, homeDiscoveryCacheTTL).Err(); err != nil {
		recordHomeCache("discovery", "write_error")
		return
	}
	recordHomeCache("discovery", "write_success")
}

func (s *Service) rankedHomeDiscovery(ctx context.Context, sql string, args ...interface{}) ([]homeDiscoveryCandidateRow, error) {
	rows := make([]homeDiscoveryCandidateRow, 0, homeDiscoveryCandidate)
	if err := s.db.WithContext(ctx).Raw(sql, args...).Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (s *Service) loadHomeDiscovery(ctx context.Context, start, now int64, timezone string) (homeDiscoveryResponse, error) {
	eligible := fmt.Sprintf(`a.short_id IS NOT NULL AND BTRIM(a.agent_name) <> ''
		AND COALESCE(a.email, '') NOT LIKE '%%@pgc.eigenflux.one'
		AND COALESCE(a.email, '') NOT LIKE '%%@bot.eigenflux.one'
		AND a.agent_id <> %d`, eigenfluxOfficialAssistantID)

	recognition, err := s.rankedHomeDiscovery(ctx, fmt.Sprintf(`
		SELECT a.agent_id, COUNT(DISTINCT f.agent_id) AS primary_value,
		       COUNT(DISTINCT f.item_id) AS secondary_value, '' AS dimension
		FROM feedback_logs f JOIN raw_items r ON r.item_id=f.item_id
		JOIN agents a ON a.agent_id=r.author_agent_id
		WHERE f.feedback_at >= ? AND f.score > 0 AND %s
		GROUP BY a.agent_id ORDER BY primary_value DESC, secondary_value DESC, a.agent_id ASC LIMIT ?`, eligible), start, homeDiscoveryCandidate)
	if err != nil {
		return homeDiscoveryResponse{}, err
	}

	broadcasts, err := s.rankedHomeDiscovery(ctx, fmt.Sprintf(`
		SELECT a.agent_id, COUNT(*) AS primary_value, COALESCE(SUM(s.consumed_count),0) AS secondary_value, '' AS dimension
		FROM raw_items r JOIN processed_items p ON p.item_id=r.item_id AND p.status=3
		JOIN agents a ON a.agent_id=r.author_agent_id LEFT JOIN item_stats s ON s.item_id=r.item_id
		WHERE r.created_at >= ? AND %s GROUP BY a.agent_id
		ORDER BY primary_value DESC, secondary_value DESC, a.agent_id ASC LIMIT ?`, eligible), start, homeDiscoveryCandidate)
	if err != nil {
		return homeDiscoveryResponse{}, err
	}

	relations, err := s.rankedHomeDiscovery(ctx, fmt.Sprintf(`
		SELECT a.agent_id, COUNT(*) AS primary_value, 0 AS secondary_value, '' AS dimension
		FROM user_relations r JOIN agents a ON a.agent_id=r.from_uid
		WHERE r.rel_type=1 AND r.created_at >= ? AND %s
		GROUP BY a.agent_id ORDER BY primary_value DESC, a.agent_id ASC LIMIT ?`, eligible), start, homeDiscoveryCandidate)
	if err != nil {
		return homeDiscoveryResponse{}, err
	}

	discussion, err := s.rankedHomeDiscovery(ctx, fmt.Sprintf(`
		SELECT a.agent_id, COUNT(DISTINCT pm.sender_id) FILTER (WHERE pm.sender_id <> a.agent_id) AS primary_value,
		       COUNT(DISTINCT c.origin_id) AS secondary_value, '' AS dimension
		FROM conversations c JOIN private_messages pm ON pm.conv_id=c.conv_id
		JOIN raw_items r ON r.item_id=c.origin_id JOIN agents a ON a.agent_id=r.author_agent_id
		WHERE c.origin_type='broadcast' AND pm.created_at >= ? AND %s
		GROUP BY a.agent_id HAVING COUNT(DISTINCT pm.sender_id) FILTER (WHERE pm.sender_id <> a.agent_id) > 0
		ORDER BY primary_value DESC, secondary_value DESC, a.agent_id ASC LIMIT ?`, eligible), start, homeDiscoveryCandidate)
	if err != nil {
		return homeDiscoveryResponse{}, err
	}

	newStandout, err := s.rankedHomeDiscovery(ctx, fmt.Sprintf(`
		SELECT a.agent_id, COUNT(DISTINCT f.agent_id) FILTER (WHERE f.score > 0) AS primary_value,
		       COUNT(DISTINCT r.item_id) AS secondary_value, '' AS dimension
		FROM agents a JOIN raw_items r ON r.author_agent_id=a.agent_id AND r.created_at >= ?
		JOIN processed_items p ON p.item_id=r.item_id AND p.status=3
		LEFT JOIN feedback_logs f ON f.item_id=r.item_id AND f.feedback_at >= ?
		WHERE a.created_at >= ? AND %s GROUP BY a.agent_id
		ORDER BY primary_value DESC, secondary_value DESC, a.created_at DESC, a.agent_id ASC LIMIT ?`, eligible), start, start, now-int64(14*24*time.Hour/time.Millisecond), homeDiscoveryCandidate)
	if err != nil {
		return homeDiscoveryResponse{}, err
	}

	domain, err := s.rankedHomeDiscovery(ctx, fmt.Sprintf(`
		WITH domain_activity AS (
		 SELECT r.author_agent_id AS agent_id, BTRIM(tag) AS dimension, COUNT(*) AS broadcasts,
		        COALESCE(SUM(s.score_1_count+s.score_2_count),0) AS helpful,
		        AVG(COALESCE(p.quality_score,0)) AS quality
		 FROM raw_items r JOIN processed_items p ON p.item_id=r.item_id AND p.status=3
		 LEFT JOIN item_stats s ON s.item_id=r.item_id
		 CROSS JOIN LATERAL regexp_split_to_table(COALESCE(p.domains,''), '[,;|]') tag
		 WHERE r.created_at >= ? AND BTRIM(tag) <> '' GROUP BY r.author_agent_id, BTRIM(tag)),
		ranked AS (
		 SELECT d.*, ROW_NUMBER() OVER (PARTITION BY d.dimension ORDER BY d.helpful DESC, d.quality DESC, d.broadcasts DESC, d.agent_id ASC) AS domain_rank
		 FROM domain_activity d)
		SELECT a.agent_id, r.helpful AS primary_value, r.broadcasts AS secondary_value, r.dimension
		FROM ranked r JOIN agents a ON a.agent_id=r.agent_id WHERE r.domain_rank=1 AND %s
		ORDER BY primary_value DESC, r.quality DESC, secondary_value DESC, a.agent_id ASC LIMIT ?`, eligible), start, homeDiscoveryCandidate)
	if err != nil {
		return homeDiscoveryResponse{}, err
	}

	rules := []homeDiscoveryRule{
		{Key: "most_recognized_today", MetricKey: "recognizing_agents_today", Rows: recognition},
		{Key: "most_active_broadcaster_today", MetricKey: "broadcasts_today", Rows: broadcasts},
		{Key: "fastest_relationship_growth_today", MetricKey: "relationships_today", Rows: relations},
		{Key: "most_discussed_today", MetricKey: "discussion_agents_today", Rows: discussion},
		{Key: "new_and_standout", MetricKey: "recognizing_agents_today", Rows: newStandout},
		{Key: "domain_representative_today", MetricKey: "recognitions_in_domain_today", Rows: domain},
	}
	selected := selectUniqueHomeDiscovery(rules, homeDiscoveryResultLimit)
	items, err := s.hydrateHomeDiscovery(ctx, selected)
	if err != nil {
		return homeDiscoveryResponse{}, err
	}
	return homeDiscoveryResponse{Items: items, WindowStart: start, WindowTimezone: timezone, GeneratedAt: now, CacheTTLSeconds: int64(homeDiscoveryCacheTTL / time.Second)}, nil
}

func selectUniqueHomeDiscovery(rules []homeDiscoveryRule, limit int) []homeDiscoveryRule {
	used := map[int64]struct{}{}
	selected := make([]homeDiscoveryRule, 0, limit)
	nextCandidate := make([]int, len(rules))
	for len(selected) < limit {
		progressed := false
		for ruleIndex, rule := range rules {
			for nextCandidate[ruleIndex] < len(rule.Rows) {
				row := rule.Rows[nextCandidate[ruleIndex]]
				nextCandidate[ruleIndex]++
				if _, exists := used[row.AgentID]; exists || row.AgentID <= 0 || row.AgentID == eigenfluxOfficialAssistantID {
					continue
				}
				used[row.AgentID] = struct{}{}
				rule.Rows = []homeDiscoveryCandidateRow{row}
				selected = append(selected, rule)
				progressed = true
				break
			}
			if len(selected) == limit {
				break
			}
		}
		if !progressed {
			break
		}
	}
	return selected
}

func (s *Service) hydrateHomeDiscovery(ctx context.Context, selected []homeDiscoveryRule) ([]homeDiscoveryAgent, error) {
	if len(selected) == 0 {
		return []homeDiscoveryAgent{}, nil
	}
	ids := make([]int64, 0, len(selected))
	for _, rule := range selected {
		ids = append(ids, rule.Rows[0].AgentID)
	}
	var rows []homeDiscoveryIdentityRow
	if err := s.db.WithContext(ctx).Raw(`SELECT a.agent_id, COALESCE(a.short_id,'') AS short_id, a.agent_name,
		COALESCE(a.agent_name_en,'') AS agent_name_en,
		COALESCE(c.public_card,'{}'::jsonb)::text AS public_card,
		COALESCE(c.private_card,'{}'::jsonb)::text AS private_card, a.created_at
		FROM agents a LEFT JOIN agent_cards c ON c.agent_id=a.agent_id
		WHERE a.agent_id = ANY(?)`, pq.Array(ids)).Scan(&rows).Error; err != nil {
		return nil, err
	}
	identities := make(map[int64]homeDiscoveryIdentityRow, len(rows))
	for _, row := range rows {
		identities[row.AgentID] = row
	}
	result := make([]homeDiscoveryAgent, 0, len(selected))
	for _, rule := range selected {
		candidate := rule.Rows[0]
		identity, ok := identities[candidate.AgentID]
		if !ok || identity.ShortID == "" {
			continue
		}
		card := map[string]interface{}{}
		_ = json.Unmarshal([]byte(identity.PublicCard), &card)
		result = append(result, homeDiscoveryAgent{
			RuleKey: rule.Key, AgentID: strconv.FormatInt(identity.AgentID, 10), ShortID: identity.ShortID,
			SharePath: "/agent/" + identity.ShortID, AgentName: identity.AgentName, AgentNameEn: identity.AgentNameEn,
			CountryCode: homeDiscoveryCountryCode(identity.PrivateCard), AgentDescription: cardString(card, "agent_description"),
			HumanDescription: cardString(card, "human_description"), Capabilities: cardStrings(card, "capabilities", 3),
			Runtime: cardString(card, "runtime"), JoinedAt: identity.CreatedAt,
			Metric: homeDiscoveryMetric{Key: rule.MetricKey, Value: candidate.Primary, Secondary: candidate.Secondary, Dimension: candidate.Dimension},
		})
	}
	return result, nil
}

func cardString(card map[string]interface{}, key string) string {
	value, _ := card[key].(string)
	return strings.TrimSpace(value)
}

func cardStrings(card map[string]interface{}, key string, limit int) []string {
	raw, _ := card[key].([]interface{})
	result := make([]string, 0, len(raw))
	for _, item := range raw {
		value, ok := item.(string)
		trimmed := strings.TrimSpace(value)
		if !ok || trimmed == "" || isUnsetPublicCardValue(trimmed) {
			continue
		}
		result = append(result, trimmed)
		if len(result) == limit {
			break
		}
	}
	return result
}

func isUnsetPublicCardValue(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "尚未设置", "暂未设置", "not set", "not set yet":
		return true
	default:
		return false
	}
}
