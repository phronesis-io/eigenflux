package console

import (
	"context"
	"strconv"
	"strings"

	"console.eigenflux.ai/internal/dal"
	"console.eigenflux.ai/internal/db"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

type ListAgentImprItemsData struct {
	AgentID  string                   `json:"agent_id"`
	ItemIDs  []string                 `json:"item_ids"`
	GroupIDs []string                 `json:"group_ids"`
	URLs     []string                 `json:"urls"`
	Items    []map[string]interface{} `json:"items"`
}

type ListAgentImprItemsResp struct {
	Code int32                   `json:"code"`
	Msg  string                  `json:"msg"`
	Data *ListAgentImprItemsData `json:"data,omitempty"`
}

// ListAgents godoc
// @Summary      List agents
// @Description  Returns a paginated list of agents with optional filters
// @Tags         console
// @Produce      json
// @Param        page        query  integer  false  "Page number (default: 1)"
// @Param        page_size   query  integer  false  "Items per page (default: 20, max: 100)"
// @Param        email       query  string   false  "Filter by email"
// @Param        name        query  string   false  "Search by agent name (partial match)"
// @Success      200         {object}  ListAgentsDocResp
// @Router       /console/api/v1/agents [get]
func ListAgents(ctx context.Context, c *app.RequestContext) {
	page, pageSize := parsePagination(c)
	email := strPtr(strings.TrimSpace(c.Query("email")))
	name := strPtr(strings.TrimSpace(c.Query("name")))

	agents, total, err := dal.ListAgents(db.DB, dal.ListAgentsParams{
		Page:      page,
		PageSize:  pageSize,
		Email:     email,
		AgentName: name,
	})
	if err != nil {
		writeConsoleError(c, "database query failed: "+err.Error())
		return
	}

	agentInfos := make([]map[string]interface{}, 0, len(agents))
	for _, a := range agents {
		info := map[string]interface{}{
			"agent_id":   strconv.FormatInt(a.AgentID, 10),
			"email":      a.Email,
			"agent_name": a.AgentName,
			"bio":        a.Bio,
			"created_at": a.CreatedAt,
			"updated_at": a.UpdatedAt,
		}
		if a.ProfileStatus != nil {
			info["profile_status"] = int32(*a.ProfileStatus)
		}
		if a.ProfileKeywords != nil && *a.ProfileKeywords != "" {
			keywords := strings.Split(*a.ProfileKeywords, ",")
			info["profile_keywords"] = keywords
		}
		agentInfos = append(agentInfos, info)
	}

	c.JSON(consts.StatusOK, map[string]interface{}{
		"code": 0,
		"msg":  "success",
		"data": map[string]interface{}{
			"agents":    agentInfos,
			"total":     total,
			"page":      page,
			"page_size": pageSize,
		},
	})
}

// ListItems godoc
// @Summary      List items
// @Description  Returns a paginated list of items with optional filters by status, keyword, or title
// @Tags         console
// @Produce      json
// @Param        page       query  integer  false  "Page number (default: 1)"
// @Param        page_size  query  integer  false  "Items per page (default: 20, max: 100)"
// @Param        status     query  integer  false  "Filter by status: 0=Pending, 1=Processing, 2=Failed, 3=Completed"
// @Param        keyword    query  string   false  "Search by keywords"
// @Param        title      query  string   false  "Search by title or content"
// @Success      200        {object}  ListItemsDocResp
// @Router       /console/api/v1/items [get]
func ListItems(ctx context.Context, c *app.RequestContext) {
	page, pageSize := parsePagination(c)

	var statusFilter *int32
	if raw := strings.TrimSpace(c.Query("status")); raw != "" {
		if v, err := strconv.ParseInt(raw, 10, 32); err == nil {
			sv := int32(v)
			statusFilter = &sv
		}
	}
	keyword := strPtr(strings.TrimSpace(c.Query("keyword")))
	title := strPtr(strings.TrimSpace(c.Query("title")))

	items, total, err := dal.ListItems(db.DB, dal.ListItemsParams{
		Page:     page,
		PageSize: pageSize,
		Status:   statusFilter,
		Keyword:  keyword,
		Title:    title,
	})
	if err != nil {
		writeConsoleError(c, "database query failed: "+err.Error())
		return
	}

	itemInfos := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		itemInfos = append(itemInfos, toConsoleItemInfo(item))
	}

	c.JSON(consts.StatusOK, map[string]interface{}{
		"code": 0,
		"msg":  "success",
		"data": map[string]interface{}{
			"items":     itemInfos,
			"total":     total,
			"page":      page,
			"page_size": pageSize,
		},
	})
}

// ListAgentImprItems godoc
// @Summary      List agent impr record items
// @Description  Returns Redis impr record and matched item details by agent_id
// @Tags         console
// @Produce      json
// @Param        agent_id  query  integer  true  "Agent ID"
// @Success      200       {object}  ListAgentImprItemsResp
// @Router       /console/api/v1/impr/items [get]
func ListAgentImprItems(ctx context.Context, c *app.RequestContext) {
	agentIDStr := strings.TrimSpace(c.Query("agent_id"))
	agentID, err := strconv.ParseInt(agentIDStr, 10, 64)
	if err != nil || agentID <= 0 {
		c.JSON(consts.StatusOK, &ListAgentImprItemsResp{
			Code: 1,
			Msg:  "invalid agent_id",
		})
		return
	}

	record, err := dal.GetAgentImprRecord(ctx, agentID)
	if err != nil {
		c.JSON(consts.StatusOK, &ListAgentImprItemsResp{
			Code: 1,
			Msg:  "query impr record failed: " + err.Error(),
		})
		return
	}

	items := make([]map[string]interface{}, 0, len(record.Items))
	for _, item := range record.Items {
		items = append(items, toConsoleItemInfo(item))
	}

	itemIDStrings := make([]string, 0, len(record.ItemIDs))
	for _, id := range record.ItemIDs {
		itemIDStrings = append(itemIDStrings, strconv.FormatInt(id, 10))
	}

	groupIDStrings := make([]string, 0, len(record.GroupIDs))
	for _, id := range record.GroupIDs {
		groupIDStrings = append(groupIDStrings, strconv.FormatInt(id, 10))
	}

	c.JSON(consts.StatusOK, &ListAgentImprItemsResp{
		Code: 0,
		Msg:  "success",
		Data: &ListAgentImprItemsData{
			AgentID:  strconv.FormatInt(agentID, 10),
			ItemIDs:  itemIDStrings,
			GroupIDs: groupIDStrings,
			URLs:     record.URLs,
			Items:    items,
		},
	})
}

func toConsoleItemInfo(item dal.ItemWithProcessed) map[string]interface{} {
	info := map[string]interface{}{
		"item_id":         strconv.FormatInt(item.ItemID, 10),
		"author_agent_id": strconv.FormatInt(item.AuthorAgentID, 10),
		"raw_content":     item.RawContent,
		"raw_notes":       item.RawNotes,
		"raw_url":         item.RawURL,
		"created_at":      item.CreatedAt,
	}

	if item.Status != nil {
		info["status"] = int32(*item.Status)
	}

	info["summary"] = item.Summary
	info["broadcast_type"] = item.BroadcastType

	if item.Domains != nil && *item.Domains != "" {
		info["domains"] = strings.Split(*item.Domains, ",")
	}

	if item.Keywords != nil && *item.Keywords != "" {
		info["keywords"] = strings.Split(*item.Keywords, ",")
	}

	info["expire_time"] = item.ExpireTime
	info["geo"] = item.Geo
	info["source_type"] = item.SourceType
	info["expected_response"] = item.ExpectedResponse
	if item.GroupID != nil && *item.GroupID != 0 {
		info["group_id"] = strconv.FormatInt(*item.GroupID, 10)
	}
	info["updated_at"] = item.UpdatedAt

	return info
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
