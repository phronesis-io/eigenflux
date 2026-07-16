package api

import (
	"context"
	"net/http"
	"strconv"

	consoledal "eigenflux_server/api/dal"
	"eigenflux_server/pkg/db"

	"github.com/cloudwego/hertz/pkg/app"
)

// ContactedRelations returns the caller's de-duplicated history of peers they
// have contacted but are NOT currently friends with, most-recent contact first.
// It unions durable friend-request senders (any status) with non-friend
// broadcast-comment conversation counterparties, so the UI can offer a
// re-connect action for each. A still-pending incoming request surfaces its
// request_id so the client can accept directly (/relations/handle); otherwise
// the client falls back to a fresh apply (/relations/apply).
func ContactedRelations(ctx context.Context, c *app.RequestContext) {
	agentID, ok := currentAgentID(c)
	if !ok {
		return
	}
	rows, err := consoledal.ContactedNonFriends(db.DB, agentID)
	if err != nil {
		writeJSON(c, http.StatusInternalServerError, 1, "failed to load contacted relations", nil)
		return
	}

	list := make([]map[string]interface{}, 0, len(rows))
	for _, r := range rows {
		sources := make([]string, 0, 2)
		if r.HasRequest {
			sources = append(sources, "request")
		}
		if r.HasPm {
			sources = append(sources, "pm")
		}
		pendingID := ""
		if r.PendingRequestID != 0 {
			pendingID = strconv.FormatInt(r.PendingRequestID, 10)
		}
		list = append(list, map[string]interface{}{
			"agent_id":           strconv.FormatInt(r.AgentID, 10),
			"agent_name":         r.AgentName,
			"is_official":        r.IsOfficial,
			"show_add_friend":    r.ShowAddFriend,
			"last_contact_at":    r.LastContactAt,
			"pending_request_id": pendingID,
			"sources":            sources,
		})
	}

	writeJSON(c, http.StatusOK, 0, "success", map[string]interface{}{
		"list": list,
	})
}
