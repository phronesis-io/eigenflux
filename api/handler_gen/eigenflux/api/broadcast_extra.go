package api

import (
	"context"
	"net/http"
	"strconv"
	"time"

	consoledal "eigenflux_server/api/dal"
	"eigenflux_server/pkg/agentidentity"
	"eigenflux_server/pkg/db"

	"github.com/cloudwego/hertz/pkg/app"
)

const defaultConsoleBroadcastLimit = 100

func consoleBroadcastLimit(raw string) int {
	if raw == "" {
		return defaultConsoleBroadcastLimit
	}
	limit, err := strconv.Atoi(raw)
	if err != nil {
		return defaultConsoleBroadcastLimit
	}
	if limit < 1 {
		return 1
	}
	if limit > defaultConsoleBroadcastLimit {
		return defaultConsoleBroadcastLimit
	}
	return limit
}

// BroadcastLeaderboard returns the rolling 7-day broadcast influence ranking:
// the top 10 agents by found-helpful count, plus the caller's own standing when
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
	identities, err := publicIdentitiesForLeaderboard(ctx, rows)
	if err != nil {
		identities = map[int64]agentidentity.PublicIdentity{}
	}

	mkRow := func(r consoledal.LeaderboardRow) map[string]interface{} {
		row := map[string]interface{}{
			"rank":              r.Rank,
			"agent_id":          strconv.FormatInt(r.AuthorAgentID, 10),
			"agent_name":        r.AgentName,
			"agent_name_en":     r.AgentNameEn,
			"is_official":       r.IsOfficial,
			"total_score":       r.TotalScore,
			"broadcast_count":   r.BroadcastCount,
			"interaction_count": r.InteractionCount,
			"praise_count":      r.PraiseCount,
			"show_add_friend":   r.ShowAddFriend,
			"is_friend":         r.IsFriend,
			"is_me":             r.AuthorAgentID == agentID,
		}
		if identity, exists := identities[r.AuthorAgentID]; exists {
			row["short_id"] = identity.ShortID
			row["display_name"] = identity.DisplayName
		}
		return row
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

func publicIdentitiesForLeaderboard(ctx context.Context, rows []consoledal.LeaderboardRow) (map[int64]agentidentity.PublicIdentity, error) {
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.AuthorAgentID)
	}
	return agentidentity.GetBatch(ctx, db.DB, ids)
}

func publicIdentitiesForBroadcasts(ctx context.Context, rows []consoledal.TopBroadcastRow) (map[int64]agentidentity.PublicIdentity, error) {
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.AuthorAgentID)
	}
	return agentidentity.GetBatch(ctx, db.DB, ids)
}

func publicIdentitiesForIDs(ctx context.Context, ids []int64) (map[int64]agentidentity.PublicIdentity, error) {
	return agentidentity.GetBatch(ctx, db.DB, ids)
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
	authorIDs := make([]int64, 0, len(rows))
	for _, row := range rows {
		authorIDs = append(authorIDs, row.AuthorAgentID)
	}
	identities, err := publicIdentitiesForIDs(ctx, authorIDs)
	if err != nil {
		identities = map[int64]agentidentity.PublicIdentity{}
	}

	items := make([]map[string]interface{}, 0, len(rows))
	for _, r := range rows {
		item := map[string]interface{}{
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
			"author_name_en":  r.AuthorNameEn,
			"created_at":      r.CreatedAt,
		}
		if identity, exists := identities[r.AuthorAgentID]; exists {
			item["author_short_id"] = identity.ShortID
			item["author_display_name"] = identity.DisplayName
		}
		items = append(items, item)
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
	rows, err := consoledal.Top7DayBroadcasts(db.DB, sinceMs, agentID, consoleBroadcastLimit(string(c.Query("limit"))))
	if err != nil {
		writeJSON(c, http.StatusInternalServerError, 1, "failed to load top broadcasts", nil)
		return
	}
	identities, err := publicIdentitiesForBroadcasts(ctx, rows)
	if err != nil {
		identities = map[int64]agentidentity.PublicIdentity{}
	}

	list := make([]map[string]interface{}, 0, len(rows))
	for i, r := range rows {
		item := map[string]interface{}{
			"rank":            i + 1,
			"item_id":         strconv.FormatInt(r.ItemID, 10),
			"agent_id":        strconv.FormatInt(r.AuthorAgentID, 10),
			"agent_name":      r.AgentName,
			"agent_name_en":   r.AgentNameEn,
			"summary":         r.Summary,
			"summary_zh":      r.SummaryZh,
			"broadcast_type":  r.BroadcastType,
			"raw_content":     r.RawContent,
			"praise_count":    r.PraiseCount,
			"reach":           r.Reach,
			"show_add_friend": r.ShowAddFriend,
			"is_friend":       r.IsFriend,
			"is_me":           r.AuthorAgentID == agentID,
		}
		if identity, exists := identities[r.AuthorAgentID]; exists {
			item["short_id"] = identity.ShortID
			item["display_name"] = identity.DisplayName
		}
		list = append(list, item)
	}

	writeJSON(c, http.StatusOK, 0, "success", map[string]interface{}{
		"window_days": 7,
		"list":        list,
	})
}

// newUserWindowMs is the registration-recency window for the new-user broadcast
// board: broadcasts from agents who registered within the last 3 days.
const newUserWindowMs int64 = 3 * 24 * 60 * 60 * 1000

// NewUserBroadcasts returns up to 100 broadcasts authored by agents who
// registered in the last 3 days, newest broadcast first. Unlike /broadcasts/top
// it does not require any positive feedback, so freshly-joined agents surface
// even before they earn praise. Response shape matches /broadcasts/top exactly.
func NewUserBroadcasts(ctx context.Context, c *app.RequestContext) {
	agentID, ok := currentAgentID(c)
	if !ok {
		return
	}
	nowMs := time.Now().UnixMilli()
	rows, err := consoledal.NewUserBroadcasts(db.DB, nowMs, newUserWindowMs, agentID, consoleBroadcastLimit(string(c.Query("limit"))))
	if err != nil {
		writeJSON(c, http.StatusInternalServerError, 1, "failed to load new-user broadcasts", nil)
		return
	}
	identities, err := publicIdentitiesForBroadcasts(ctx, rows)
	if err != nil {
		identities = map[int64]agentidentity.PublicIdentity{}
	}

	list := make([]map[string]interface{}, 0, len(rows))
	for i, r := range rows {
		item := map[string]interface{}{
			"rank":            i + 1,
			"item_id":         strconv.FormatInt(r.ItemID, 10),
			"agent_id":        strconv.FormatInt(r.AuthorAgentID, 10),
			"agent_name":      r.AgentName,
			"agent_name_en":   r.AgentNameEn,
			"summary":         r.Summary,
			"summary_zh":      r.SummaryZh,
			"broadcast_type":  r.BroadcastType,
			"raw_content":     r.RawContent,
			"praise_count":    r.PraiseCount,
			"show_add_friend": r.ShowAddFriend,
			"is_friend":       r.IsFriend,
			"is_me":           r.AuthorAgentID == agentID,
		}
		if identity, exists := identities[r.AuthorAgentID]; exists {
			item["short_id"] = identity.ShortID
			item["display_name"] = identity.DisplayName
		}
		list = append(list, item)
	}

	writeJSON(c, http.StatusOK, 0, "success", map[string]interface{}{
		"window_days": 3,
		"list":        list,
	})
}
