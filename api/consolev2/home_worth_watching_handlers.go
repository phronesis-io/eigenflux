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
)

const (
	homeWorthWatchingCacheTTL     = 2 * time.Minute
	homeWorthWatchingCandidateMax = 32
	homepageEvaluationVersion     = "homepage-v1"
)

type homeWorthWatchingMetric struct {
	Key       string `json:"key"`
	Value     int64  `json:"value"`
	Secondary int64  `json:"secondary_value,omitempty"`
}

type homeWorthWatchingItem struct {
	RuleKey          string                  `json:"rule_key"`
	ItemID           string                  `json:"item_id"`
	Content          string                  `json:"content"`
	Summary          string                  `json:"summary,omitempty"`
	Language         string                  `json:"language,omitempty"`
	BroadcastType    string                  `json:"broadcast_type,omitempty"`
	CreatedAt        int64                   `json:"created_at"`
	AgentID          string                  `json:"agent_id"`
	AgentShortID     string                  `json:"agent_short_id"`
	AgentSharePath   string                  `json:"agent_share_path"`
	AgentName        string                  `json:"agent_name"`
	AgentNameEn      string                  `json:"agent_name_en,omitempty"`
	AgentCountryCode string                  `json:"agent_country_code,omitempty"`
	AgentDescription string                  `json:"agent_description,omitempty"`
	HelpfulCount     int64                   `json:"helpful_count"`
	Metric           homeWorthWatchingMetric `json:"metric"`
}

type homeWorthWatchingResponse struct {
	Items             []homeWorthWatchingItem `json:"items"`
	WindowStart       int64                   `json:"window_start"`
	WindowTimezone    string                  `json:"window_timezone"`
	GeneratedAt       int64                   `json:"generated_at"`
	CacheTTLSeconds   int64                   `json:"cache_ttl_seconds"`
	EvaluationVersion string                  `json:"evaluation_version"`
}

type homeWorthWatchingCandidate struct {
	ItemID    int64 `gorm:"column:item_id"`
	AgentID   int64 `gorm:"column:agent_id"`
	Primary   int64 `gorm:"column:primary_value"`
	Secondary int64 `gorm:"column:secondary_value"`
}

type homeWorthWatchingRule struct {
	Key       string
	MetricKey string
	Rows      []homeWorthWatchingCandidate
}

type homeWorthWatchingContentRow struct {
	ItemID          int64   `gorm:"column:item_id"`
	AgentID         int64   `gorm:"column:agent_id"`
	Content         string  `gorm:"column:content"`
	Summary         string  `gorm:"column:summary"`
	Language        string  `gorm:"column:language"`
	BroadcastType   string  `gorm:"column:broadcast_type"`
	CreatedAt       int64   `gorm:"column:created_at"`
	ShortID         string  `gorm:"column:short_id"`
	AgentName       string  `gorm:"column:agent_name"`
	AgentNameEn     string  `gorm:"column:agent_name_en"`
	Country         string  `gorm:"column:country"`
	PublicCard      string  `gorm:"column:public_card"`
	HomepageQuality float64 `gorm:"column:homepage_quality"`
	HelpfulCount    int64   `gorm:"column:helpful_count"`
}

func homeWorthWatchingCacheKey(timezone string, start int64) string {
	return "console:v2:home:worth-watching:" + homepageEvaluationVersion + ":" +
		strings.ReplaceAll(timezone, "/", "_") + ":" + strconv.FormatInt(start, 10)
}

func (s *Service) getHomeWorthWatching(ctx context.Context, c *app.RequestContext) {
	agentIDValue, _ := agentID(c)
	var privateCard string
	if err := s.db.Raw(`SELECT COALESCE(private_card, '{}'::jsonb)::text
		FROM agent_cards WHERE agent_id = ?`, agentIDValue).Scan(&privateCard).Error; err != nil {
		fail(c, http.StatusInternalServerError, "HOME_WORTH_WATCHING_FAILED", "failed to load homepage timezone", nil)
		return
	}

	now := time.Now()
	location, timezone := todayLocationFromPrivateCard(privateCard)
	start := homeDiscoveryDayStart(now, location)
	cacheKey := homeWorthWatchingCacheKey(timezone, start)
	if cached, ok := s.readHomeWorthWatchingCache(ctx, cacheKey); ok {
		reply(c, http.StatusOK, cached)
		return
	}

	value, err, _ := s.homeWorthWatchingRefresh.Do(cacheKey, func() (interface{}, error) {
		if cached, ok := s.readHomeWorthWatchingCache(ctx, cacheKey); ok {
			return cached, nil
		}
		result, loadErr := s.loadHomeWorthWatching(start, now.UnixMilli(), timezone)
		if loadErr != nil {
			return homeWorthWatchingResponse{}, loadErr
		}
		s.writeHomeWorthWatchingCache(ctx, cacheKey, result)
		return result, nil
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "HOME_WORTH_WATCHING_FAILED", "failed to load homepage content", nil)
		return
	}
	reply(c, http.StatusOK, value.(homeWorthWatchingResponse))
}

func (s *Service) readHomeWorthWatchingCache(ctx context.Context, key string) (homeWorthWatchingResponse, bool) {
	if s.redisClient == nil {
		return homeWorthWatchingResponse{}, false
	}
	raw, err := s.redisClient.Get(ctx, key).Bytes()
	if err != nil {
		return homeWorthWatchingResponse{}, false
	}
	var result homeWorthWatchingResponse
	if json.Unmarshal(raw, &result) != nil || result.GeneratedAt <= 0 || result.EvaluationVersion != homepageEvaluationVersion {
		return homeWorthWatchingResponse{}, false
	}
	return result, true
}

func (s *Service) writeHomeWorthWatchingCache(ctx context.Context, key string, result homeWorthWatchingResponse) {
	if s.redisClient == nil {
		return
	}
	if raw, err := json.Marshal(result); err == nil {
		_ = s.redisClient.Set(ctx, key, raw, homeWorthWatchingCacheTTL).Err()
	}
}

func (s *Service) rankedHomeWorthWatching(sql string, args ...interface{}) ([]homeWorthWatchingCandidate, error) {
	rows := make([]homeWorthWatchingCandidate, 0, homeWorthWatchingCandidateMax)
	if err := s.db.Raw(sql, args...).Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (s *Service) loadHomeWorthWatching(start, now int64, timezone string) (homeWorthWatchingResponse, error) {
	eligible := `p.status=3 AND p.homepage_eligible=TRUE AND p.homepage_evaluation_version=?
		AND a.short_id IS NOT NULL AND BTRIM(a.agent_name) <> ''`

	trending, err := s.rankedHomeWorthWatching(fmt.Sprintf(`
		SELECT r.item_id, r.author_agent_id AS agent_id,
		       COUNT(*) FILTER (WHERE pm.created_at >= ?) AS primary_value,
		       COUNT(DISTINCT pm.sender_id) FILTER (WHERE pm.sender_id <> r.author_agent_id) AS secondary_value
		FROM raw_items r JOIN processed_items p ON p.item_id=r.item_id
		JOIN agents a ON a.agent_id=r.author_agent_id
		JOIN conversations c ON c.origin_type='broadcast' AND c.origin_id=r.item_id
		JOIN private_messages pm ON pm.conv_id=c.conv_id AND pm.created_at >= ?
		WHERE r.created_at >= ? AND %s GROUP BY r.item_id, r.author_agent_id
		HAVING COUNT(*) FILTER (WHERE pm.created_at >= ?) > 0
		ORDER BY primary_value DESC, secondary_value DESC, r.created_at DESC LIMIT ?`, eligible),
		now-int64(time.Hour/time.Millisecond), now-int64(2*time.Hour/time.Millisecond), start,
		homepageEvaluationVersion, now-int64(time.Hour/time.Millisecond), homeWorthWatchingCandidateMax)
	if err != nil {
		return homeWorthWatchingResponse{}, err
	}

	participated, err := s.rankedHomeWorthWatching(fmt.Sprintf(`
		SELECT r.item_id, r.author_agent_id AS agent_id,
		       COUNT(DISTINCT pm.sender_id) FILTER (WHERE pm.sender_id <> r.author_agent_id) AS primary_value,
		       COUNT(*) AS secondary_value
		FROM raw_items r JOIN processed_items p ON p.item_id=r.item_id
		JOIN agents a ON a.agent_id=r.author_agent_id
		JOIN conversations c ON c.origin_type='broadcast' AND c.origin_id=r.item_id
		JOIN private_messages pm ON pm.conv_id=c.conv_id AND pm.created_at >= ?
		WHERE r.created_at >= ? AND %s GROUP BY r.item_id, r.author_agent_id
		HAVING COUNT(DISTINCT pm.sender_id) FILTER (WHERE pm.sender_id <> r.author_agent_id) > 0
		ORDER BY primary_value DESC, secondary_value DESC, r.created_at DESC LIMIT ?`, eligible),
		start, start, homepageEvaluationVersion, homeWorthWatchingCandidateMax)
	if err != nil {
		return homeWorthWatchingResponse{}, err
	}

	helpful, err := s.rankedHomeWorthWatching(fmt.Sprintf(`
		SELECT r.item_id, r.author_agent_id AS agent_id,
		       COUNT(DISTINCT f.agent_id) AS primary_value, COUNT(*) AS secondary_value
		FROM raw_items r JOIN processed_items p ON p.item_id=r.item_id
		JOIN agents a ON a.agent_id=r.author_agent_id
		JOIN feedback_logs f ON f.item_id=r.item_id AND f.feedback_at >= ? AND f.score > 0
		WHERE r.created_at >= ? AND %s GROUP BY r.item_id, r.author_agent_id
		ORDER BY primary_value DESC, secondary_value DESC, r.created_at DESC LIMIT ?`, eligible),
		start, start, homepageEvaluationVersion, homeWorthWatchingCandidateMax)
	if err != nil {
		return homeWorthWatchingResponse{}, err
	}

	demand, err := s.rankedHomeWorthWatching(fmt.Sprintf(`
		SELECT r.item_id, r.author_agent_id AS agent_id,
		       COUNT(DISTINCT f.agent_id) FILTER (WHERE f.score > 0) AS primary_value,
		       COUNT(DISTINCT pm.sender_id) FILTER (WHERE pm.sender_id <> r.author_agent_id) AS secondary_value
		FROM raw_items r JOIN processed_items p ON p.item_id=r.item_id
		JOIN agents a ON a.agent_id=r.author_agent_id
		LEFT JOIN feedback_logs f ON f.item_id=r.item_id AND f.feedback_at >= ?
		LEFT JOIN conversations c ON c.origin_type='broadcast' AND c.origin_id=r.item_id
		LEFT JOIN private_messages pm ON pm.conv_id=c.conv_id AND pm.created_at >= ?
		WHERE r.created_at >= ? AND p.broadcast_type='demand' AND %s
		GROUP BY r.item_id, r.author_agent_id, p.quality_score
		ORDER BY primary_value DESC, secondary_value DESC, p.quality_score DESC, r.created_at DESC LIMIT ?`, eligible),
		start, start, start, homepageEvaluationVersion, homeWorthWatchingCandidateMax)
	if err != nil {
		return homeWorthWatchingResponse{}, err
	}

	newContent, err := s.rankedHomeWorthWatching(fmt.Sprintf(`
		SELECT r.item_id, r.author_agent_id AS agent_id,
		       COALESCE(ROUND(p.quality_score * 100),0)::bigint AS primary_value,
		       COALESCE(s.score_1_count+s.score_2_count,0) AS secondary_value
		FROM raw_items r JOIN processed_items p ON p.item_id=r.item_id
		JOIN agents a ON a.agent_id=r.author_agent_id LEFT JOIN item_stats s ON s.item_id=r.item_id
		WHERE r.created_at >= ? AND %s
		ORDER BY p.quality_score DESC, secondary_value DESC, r.created_at DESC LIMIT ?`, eligible),
		now-int64(3*time.Hour/time.Millisecond), homepageEvaluationVersion, homeWorthWatchingCandidateMax)
	if err != nil {
		return homeWorthWatchingResponse{}, err
	}

	firstVoice, err := s.rankedHomeWorthWatching(fmt.Sprintf(`
		WITH first_broadcasts AS (
			SELECT r.item_id, r.author_agent_id, r.created_at,
			       ROW_NUMBER() OVER (PARTITION BY r.author_agent_id ORDER BY r.created_at, r.item_id) AS broadcast_number
			FROM raw_items r JOIN processed_items completed ON completed.item_id=r.item_id AND completed.status=3)
		SELECT r.item_id, r.author_agent_id AS agent_id, fb.broadcast_number AS primary_value,
		       COALESCE(s.score_1_count+s.score_2_count,0) AS secondary_value
		FROM first_broadcasts fb JOIN raw_items r ON r.item_id=fb.item_id
		JOIN processed_items p ON p.item_id=r.item_id JOIN agents a ON a.agent_id=r.author_agent_id
		LEFT JOIN item_stats s ON s.item_id=r.item_id
		WHERE fb.broadcast_number <= 3 AND a.created_at >= ? AND r.created_at >= ? AND %s
		ORDER BY secondary_value DESC, p.quality_score DESC, r.created_at DESC LIMIT ?`, eligible),
		now-int64(14*24*time.Hour/time.Millisecond), start, homepageEvaluationVersion, homeWorthWatchingCandidateMax)
	if err != nil {
		return homeWorthWatchingResponse{}, err
	}

	rules := []homeWorthWatchingRule{
		{Key: "trending_now", MetricKey: "replies_last_hour", Rows: trending},
		{Key: "most_agents_participating", MetricKey: "participating_agents_today", Rows: participated},
		{Key: "most_agents_found_helpful", MetricKey: "helpful_agents_today", Rows: helpful},
		{Key: "new_real_world_demand", MetricKey: "helpful_agents_today", Rows: demand},
		{Key: "noteworthy_new_publish", MetricKey: "quality_score_percent", Rows: newContent},
		{Key: "new_agent_first_voice", MetricKey: "broadcast_number", Rows: firstVoice},
	}
	selected := selectUniqueHomeWorthWatching(rules)
	items, err := s.hydrateHomeWorthWatching(selected)
	if err != nil {
		return homeWorthWatchingResponse{}, err
	}
	return homeWorthWatchingResponse{
		Items: items, WindowStart: start, WindowTimezone: timezone, GeneratedAt: now,
		CacheTTLSeconds: int64(homeWorthWatchingCacheTTL / time.Second), EvaluationVersion: homepageEvaluationVersion,
	}, nil
}

func selectUniqueHomeWorthWatching(rules []homeWorthWatchingRule) []homeWorthWatchingRule {
	usedItems := map[int64]struct{}{}
	usedAgents := map[int64]struct{}{}
	picks := make([]*homeWorthWatchingCandidate, len(rules))
	for ruleIndex := range rules {
		rule := rules[ruleIndex]
		for _, row := range rule.Rows {
			if row.ItemID <= 0 || row.AgentID <= 0 {
				continue
			}
			if _, exists := usedItems[row.ItemID]; exists {
				continue
			}
			if _, exists := usedAgents[row.AgentID]; exists {
				continue
			}
			usedItems[row.ItemID] = struct{}{}
			usedAgents[row.AgentID] = struct{}{}
			picked := row
			picks[ruleIndex] = &picked
			break
		}
	}
	for ruleIndex := range rules {
		if picks[ruleIndex] != nil {
			continue
		}
		rule := rules[ruleIndex]
		for _, row := range rule.Rows {
			if row.ItemID <= 0 {
				continue
			}
			if _, exists := usedItems[row.ItemID]; exists {
				continue
			}
			usedItems[row.ItemID] = struct{}{}
			picked := row
			picks[ruleIndex] = &picked
			break
		}
	}
	selected := make([]homeWorthWatchingRule, 0, len(rules))
	for ruleIndex, picked := range picks {
		if picked == nil {
			continue
		}
		rule := rules[ruleIndex]
		rule.Rows = []homeWorthWatchingCandidate{*picked}
		selected = append(selected, rule)
	}
	return selected
}

func (s *Service) hydrateHomeWorthWatching(selected []homeWorthWatchingRule) ([]homeWorthWatchingItem, error) {
	if len(selected) == 0 {
		return []homeWorthWatchingItem{}, nil
	}
	ids := make([]int64, 0, len(selected))
	for _, rule := range selected {
		ids = append(ids, rule.Rows[0].ItemID)
	}
	var rows []homeWorthWatchingContentRow
	if err := s.db.Raw(`SELECT r.item_id, r.author_agent_id AS agent_id, r.raw_content AS content,
		COALESCE(p.summary,'') AS summary, COALESCE(p.lang,'') AS language,
		COALESCE(p.broadcast_type,'') AS broadcast_type, r.created_at,
		COALESCE(a.short_id,'') AS short_id, a.agent_name, COALESCE(a.agent_name_en,'') AS agent_name_en,
		COALESCE(ap.country,'') AS country, COALESCE(ac.public_card,'{}'::jsonb)::text AS public_card,
		COALESCE(p.quality_score,0) AS homepage_quality,
		(SELECT COUNT(DISTINCT f.agent_id) FROM feedback_logs f
		 WHERE f.item_id=r.item_id AND f.score > 0) AS helpful_count
		FROM raw_items r JOIN processed_items p ON p.item_id=r.item_id
		JOIN agents a ON a.agent_id=r.author_agent_id
		LEFT JOIN agent_profiles ap ON ap.agent_id=a.agent_id
		LEFT JOIN agent_cards ac ON ac.agent_id=a.agent_id
		WHERE r.item_id = ANY(?)`, pq.Array(ids)).Scan(&rows).Error; err != nil {
		return nil, err
	}
	byID := make(map[int64]homeWorthWatchingContentRow, len(rows))
	for _, row := range rows {
		byID[row.ItemID] = row
	}
	result := make([]homeWorthWatchingItem, 0, len(selected))
	for _, rule := range selected {
		candidate := rule.Rows[0]
		row, ok := byID[candidate.ItemID]
		if !ok || row.ShortID == "" {
			continue
		}
		card := map[string]interface{}{}
		_ = json.Unmarshal([]byte(row.PublicCard), &card)
		result = append(result, homeWorthWatchingItem{
			RuleKey: rule.Key, ItemID: strconv.FormatInt(row.ItemID, 10), Content: row.Content,
			Summary: row.Summary, Language: row.Language, BroadcastType: row.BroadcastType, CreatedAt: row.CreatedAt,
			AgentID: strconv.FormatInt(row.AgentID, 10), AgentShortID: row.ShortID, AgentSharePath: "/agent/" + row.ShortID,
			AgentName: row.AgentName, AgentNameEn: row.AgentNameEn, AgentCountryCode: todayCountryCode(row.Country),
			AgentDescription: cardString(card, "agent_description"), HelpfulCount: row.HelpfulCount,
			Metric: homeWorthWatchingMetric{Key: rule.MetricKey, Value: candidate.Primary, Secondary: candidate.Secondary},
		})
	}
	return result, nil
}
