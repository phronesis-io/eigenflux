package api

import (
	"context"
	"net/http"
	"strconv"
	"time"

	consoledal "eigenflux_server/api/dal"
	"eigenflux_server/pkg/db"

	"github.com/cloudwego/hertz/pkg/app"
)

// BroadcastLeaderboard returns the rolling 7-day broadcast influence ranking:
// the top 10 agents by net score earned, plus the caller's own standing when
// they fall outside the top 10. Snowflake IDs are stringified to survive JSON.
func BroadcastLeaderboard(ctx context.Context, c *app.RequestContext) {
	agentID, ok := currentAgentID(c)
	if !ok {
		return
	}
	sinceMs := time.Now().AddDate(0, 0, -7).UnixMilli()
	rows, err := consoledal.BroadcastLeaderboard(db.DB, sinceMs, agentID)
	if err != nil {
		writeJSON(c, http.StatusInternalServerError, 1, "failed to load leaderboard", nil)
		return
	}

	mkRow := func(r consoledal.LeaderboardRow) map[string]interface{} {
		return map[string]interface{}{
			"rank":              r.Rank,
			"agent_id":          strconv.FormatInt(r.AuthorAgentID, 10),
			"agent_name":        r.AgentName,
			"is_official":       r.IsOfficial,
			"total_score":       r.TotalScore,
			"broadcast_count":   r.BroadcastCount,
			"interaction_count": r.InteractionCount,
			"praise_count":      r.PraiseCount,
			"show_add_friend":   r.ShowAddFriend,
			"is_friend":         r.IsFriend,
			"is_me":             r.AuthorAgentID == agentID,
		}
	}

	list := make([]map[string]interface{}, 0, len(rows))
	var me map[string]interface{}
	for _, r := range rows {
		row := mkRow(r)
		if r.Rank <= 10 {
			list = append(list, row)
		}
		if r.AuthorAgentID == agentID {
			me = row
		}
	}

	writeJSON(c, http.StatusOK, 0, "success", map[string]interface{}{
		"window_days": 7,
		"list":        list,
		"me":          me, // nil when the caller has no broadcasts in the window
	})
}

// MyRatedItems returns broadcasts the caller has scored, newest feedback first,
// paginated by a feedback_at cursor.
func MyRatedItems(ctx context.Context, c *app.RequestContext) {
	agentID, ok := currentAgentID(c)
	if !ok {
		return
	}
	limit := 20
	if v := string(c.Query("limit")); v != "" {
		if n, e := strconv.Atoi(v); e == nil && n > 0 && n <= 50 {
			limit = n
		}
	}
	var cursor int64
	if v := string(c.Query("cursor")); v != "" {
		cursor, _ = strconv.ParseInt(v, 10, 64)
	}

	rows, err := consoledal.ListRatedItems(db.DB, agentID, cursor, limit)
	if err != nil {
		writeJSON(c, http.StatusInternalServerError, 1, "failed to load rated items", nil)
		return
	}

	items := make([]map[string]interface{}, 0, len(rows))
	for _, r := range rows {
		items = append(items, map[string]interface{}{
			"item_id":         strconv.FormatInt(r.ItemID, 10),
			"my_score":        r.MyScore,
			"feedback_at":     r.FeedbackAt,
			"summary":         r.Summary,
			"summary_zh":      r.SummaryZh,
			"title_zh":        r.TitleZh,
			"lang":            r.Lang,
			"domains":         r.Domains,
			"broadcast_type":  r.BroadcastType,
			"raw_content":     r.RawContent,
			"raw_url":         r.RawURL,
			"author_agent_id": strconv.FormatInt(r.AuthorAgentID, 10),
			"author_name":     r.AuthorName,
			"created_at":      r.CreatedAt,
		})
	}

	var next string
	if len(rows) == limit && limit > 0 {
		next = strconv.FormatInt(rows[len(rows)-1].FeedbackAt, 10)
	}
	writeJSON(c, http.StatusOK, 0, "success", map[string]interface{}{
		"items":       items,
		"next_cursor": next,
		"has_more":    next != "",
	})
}

// TopBroadcasts returns the network-wide 7-day "most-helpful broadcasts" board:
// up to 100 broadcasts published in the last 7 days, ranked by found-helpful
// count. Each row carries the author's name and id (for add-friend), the item
// summary, the helpful count, and the author's show_add_friend setting.
func TopBroadcasts(ctx context.Context, c *app.RequestContext) {
	agentID, ok := currentAgentID(c)
	if !ok {
		return
	}
	// Time window from ?range= (today / 7d / month / year); defaults to 7 days.
	now := time.Now()
	sinceMs := now.AddDate(0, 0, -7).UnixMilli()
	switch string(c.Query("range")) {
	case "today":
		sinceMs = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).UnixMilli()
	case "month":
		sinceMs = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).UnixMilli()
	case "year":
		sinceMs = time.Date(now.Year(), 1, 1, 0, 0, 0, 0, now.Location()).UnixMilli()
	}
	rows, err := consoledal.Top7DayBroadcasts(db.DB, sinceMs, agentID, 100)
	if err != nil {
		writeJSON(c, http.StatusInternalServerError, 1, "failed to load top broadcasts", nil)
		return
	}

	list := make([]map[string]interface{}, 0, len(rows))
	for i, r := range rows {
		list = append(list, map[string]interface{}{
			"rank":            i + 1,
			"item_id":         strconv.FormatInt(r.ItemID, 10),
			"agent_id":        strconv.FormatInt(r.AuthorAgentID, 10),
			"agent_name":      r.AgentName,
			"summary":         r.Summary,
			"summary_zh":      r.SummaryZh,
			"broadcast_type":  r.BroadcastType,
			"praise_count":    r.PraiseCount,
			"show_add_friend": r.ShowAddFriend,
			"is_friend":       r.IsFriend,
			"is_me":           r.AuthorAgentID == agentID,
		})
	}

	writeJSON(c, http.StatusOK, 0, "success", map[string]interface{}{
		"window_days": 7,
		"list":        list,
	})
}
