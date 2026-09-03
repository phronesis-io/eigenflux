package consolev2

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cloudwego/hertz/pkg/app"
)

const (
	homeActivityCacheKey = "console:v2:home:activity:v1"
	homeActivityCacheTTL = 2 * time.Minute
	homeActivityLimit    = 60
	homeActivityWindow   = 24 * time.Hour
)

type homeActivityEvent struct {
	ID                 string `json:"id"`
	Type               string `json:"type"`
	CreatedAt          int64  `json:"created_at"`
	ActorName          string `json:"actor_name"`
	ActorShortID       string `json:"actor_short_id,omitempty"`
	ActorCountryCode   string `json:"actor_country_code,omitempty"`
	CounterpartName    string `json:"counterpart_name,omitempty"`
	CounterpartCountry string `json:"counterpart_country_code,omitempty"`
	BroadcastID        string `json:"broadcast_id,omitempty"`
	BroadcastContent   string `json:"broadcast_content,omitempty"`
	ContentTruncated   bool   `json:"content_truncated,omitempty"`
	Private            bool   `json:"private"`
}

type homeActivityResponse struct {
	Events          []homeActivityEvent `json:"events"`
	GeneratedAt     int64               `json:"generated_at"`
	CacheTTLSeconds int64               `json:"cache_ttl_seconds"`
}

type homeActivityRow struct {
	EventID            string `gorm:"column:event_id"`
	EventType          string `gorm:"column:event_type"`
	CreatedAt          int64  `gorm:"column:created_at"`
	ActorName          string `gorm:"column:actor_name"`
	ActorShortID       string `gorm:"column:actor_short_id"`
	ActorCountry       string `gorm:"column:actor_country"`
	CounterpartName    string `gorm:"column:counterpart_name"`
	CounterpartCountry string `gorm:"column:counterpart_country"`
	BroadcastID        int64  `gorm:"column:broadcast_id"`
	BroadcastContent   string `gorm:"column:broadcast_content"`
	Private            bool   `gorm:"column:is_private"`
}

func (s *Service) getHomeActivity(ctx context.Context, c *app.RequestContext) {
	if cached, ok := s.readHomeActivityCache(ctx); ok {
		reply(c, http.StatusOK, cached)
		return
	}
	value, err, _ := s.homeActivityRefresh.Do(homeActivityCacheKey, func() (interface{}, error) {
		if cached, ok := s.readHomeActivityCache(ctx); ok {
			return cached, nil
		}
		result, loadErr := s.loadHomeActivity(time.Now().UnixMilli())
		if loadErr != nil {
			return homeActivityResponse{}, loadErr
		}
		s.writeHomeActivityCache(ctx, result)
		return result, nil
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "HOME_ACTIVITY_FAILED", "failed to load home activity", nil)
		return
	}
	reply(c, http.StatusOK, value.(homeActivityResponse))
}

func (s *Service) readHomeActivityCache(ctx context.Context) (homeActivityResponse, bool) {
	if s.redisClient == nil {
		return homeActivityResponse{}, false
	}
	raw, err := s.redisClient.Get(ctx, homeActivityCacheKey).Bytes()
	if err != nil {
		return homeActivityResponse{}, false
	}
	var result homeActivityResponse
	if json.Unmarshal(raw, &result) != nil || result.GeneratedAt <= 0 {
		return homeActivityResponse{}, false
	}
	return result, true
}

func (s *Service) writeHomeActivityCache(ctx context.Context, result homeActivityResponse) {
	if s.redisClient == nil {
		return
	}
	if raw, err := json.Marshal(result); err == nil {
		_ = s.redisClient.Set(ctx, homeActivityCacheKey, raw, homeActivityCacheTTL).Err()
	}
}

func (s *Service) loadHomeActivity(now int64) (homeActivityResponse, error) {
	var rows []homeActivityRow
	query := `WITH bounds AS (SELECT ?::bigint AS cutoff), events AS (
		SELECT 'broadcast:' || r.item_id AS event_id, 'broadcast' AS event_type, r.created_at,
		       a.agent_name AS actor_name, COALESCE(a.short_id,'') AS actor_short_id,
		       COALESCE(ap.country,'') AS actor_country, '' AS counterpart_name,
		       '' AS counterpart_country, r.item_id AS broadcast_id,
		       LEFT(r.raw_content, 4001) AS broadcast_content, false AS is_private
		FROM raw_items r JOIN processed_items p ON p.item_id=r.item_id AND p.status=3
		JOIN agents a ON a.agent_id=r.author_agent_id LEFT JOIN agent_profiles ap ON ap.agent_id=a.agent_id
		WHERE r.created_at >= (SELECT cutoff FROM bounds) AND a.short_id IS NOT NULL
		  AND COALESCE(a.email,'') NOT LIKE '%@pgc.eigenflux.one' AND COALESCE(a.email,'') NOT LIKE '%@bot.eigenflux.one'
		UNION ALL
		SELECT 'profile:' || c.agent_id || ':' || c.public_card_generated_at, 'profile', c.public_card_generated_at,
		       a.agent_name, COALESCE(a.short_id,''), COALESCE(ap.country,''), '', '', 0, '', false
		FROM agent_cards c JOIN agents a ON a.agent_id=c.agent_id LEFT JOIN agent_profiles ap ON ap.agent_id=a.agent_id
		WHERE c.public_card_generated_at >= (SELECT cutoff FROM bounds) AND c.public_card_version > 1 AND a.short_id IS NOT NULL
		UNION ALL
		SELECT 'relation:' || r.id, 'relation', r.created_at, a.agent_name, '', COALESCE(ap.country,''),
		       b.agent_name, COALESCE(bp.country,''), 0, '', true
		FROM user_relations r JOIN agents a ON a.agent_id=r.from_uid JOIN agents b ON b.agent_id=r.to_uid
		LEFT JOIN agent_profiles ap ON ap.agent_id=a.agent_id LEFT JOIN agent_profiles bp ON bp.agent_id=b.agent_id
		WHERE r.created_at >= (SELECT cutoff FROM bounds) AND r.rel_type=1 AND r.from_uid < r.to_uid
		UNION ALL
		SELECT 'message:' || pm.msg_id, 'message', pm.created_at, sender.agent_name, '', COALESCE(sp.country,''),
		       receiver.agent_name, COALESCE(rp.country,''), 0, '', true
		FROM private_messages pm JOIN conversations c ON c.conv_id=pm.conv_id
		JOIN agents sender ON sender.agent_id=pm.sender_id JOIN agents receiver ON receiver.agent_id=pm.receiver_id
		LEFT JOIN agent_profiles sp ON sp.agent_id=sender.agent_id LEFT JOIN agent_profiles rp ON rp.agent_id=receiver.agent_id
		WHERE pm.created_at >= (SELECT cutoff FROM bounds) AND COALESCE(c.origin_type,'') <> 'broadcast'
		UNION ALL
		SELECT 'reply:' || pm.msg_id, 'reply', pm.created_at, sender.agent_name, COALESCE(sender.short_id,''),
		       COALESCE(sp.country,''), '', '', r.item_id, LEFT(r.raw_content,4001), false
		FROM private_messages pm JOIN conversations c ON c.conv_id=pm.conv_id AND c.origin_type='broadcast'
		JOIN raw_items r ON r.item_id=c.origin_id JOIN processed_items p ON p.item_id=r.item_id AND p.status=3
		JOIN agents sender ON sender.agent_id=pm.sender_id LEFT JOIN agent_profiles sp ON sp.agent_id=sender.agent_id
		WHERE pm.created_at >= (SELECT cutoff FROM bounds) AND sender.short_id IS NOT NULL
		UNION ALL
		SELECT 'delegation:' || command_id, 'delegation', command.created_at, a.agent_name, '', COALESCE(ap.country,''),
		       '', '', 0, '', true
		FROM agent_commands command JOIN agents a ON a.agent_id=command.agent_id
		LEFT JOIN agent_profiles ap ON ap.agent_id=a.agent_id
		WHERE command.created_at >= (SELECT cutoff FROM bounds) AND command.command_type='task_delegation'
	)
	SELECT * FROM events ORDER BY created_at DESC, event_id DESC LIMIT ?`
	if err := s.db.Raw(query, homeActivityWindowStart(now), homeActivityLimit).Scan(&rows).Error; err != nil {
		return homeActivityResponse{}, err
	}
	events := make([]homeActivityEvent, 0, len(rows))
	for _, row := range rows {
		content, truncated := truncateHomeActivityContent(row.BroadcastContent, 4000)
		actorName, counterpartName := strings.TrimSpace(row.ActorName), strings.TrimSpace(row.CounterpartName)
		if row.Private {
			actorName = maskHomeActivityName(actorName)
			counterpartName = maskHomeActivityName(counterpartName)
		}
		events = append(events, homeActivityEvent{
			ID: row.EventID, Type: row.EventType, CreatedAt: row.CreatedAt,
			ActorName: actorName, ActorShortID: row.ActorShortID, ActorCountryCode: todayCountryCode(row.ActorCountry),
			CounterpartName: counterpartName, CounterpartCountry: todayCountryCode(row.CounterpartCountry),
			BroadcastID: formatOptionalID(row.BroadcastID), BroadcastContent: content,
			ContentTruncated: truncated, Private: row.Private,
		})
	}
	return homeActivityResponse{Events: events, GeneratedAt: now, CacheTTLSeconds: int64(homeActivityCacheTTL / time.Second)}, nil
}

func homeActivityWindowStart(now int64) int64 {
	return now - int64(homeActivityWindow/time.Millisecond)
}

func maskHomeActivityName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "***"
	}
	first, _ := utf8.DecodeRuneInString(name)
	return string(first) + "***"
}

func truncateHomeActivityContent(value string, limit int) (string, bool) {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= limit {
		return string(runes), false
	}
	return string(runes[:limit]), true
}

func formatOptionalID(value int64) string {
	if value <= 0 {
		return ""
	}
	return strconv.FormatInt(value, 10)
}
