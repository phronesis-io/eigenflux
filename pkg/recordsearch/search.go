// Package recordsearch defines common literal matching and pagination helpers.
// Domain services own queries and authorization; the gateway owns HTTP projection.
package recordsearch

import (
	"context"
	"strconv"
	"strings"
	"unicode/utf8"

	"eigenflux_server/kitex_gen/eigenflux/base"
	search "eigenflux_server/kitex_gen/eigenflux/recordsearch"
	"eigenflux_server/pkg/reqinfo"
)

func ValidQuery(query string) bool {
	return utf8.ValidString(query) && strings.TrimSpace(query) == query && utf8.RuneCountInString(query) >= 1 && utf8.RuneCountInString(query) <= 100 && !strings.ContainsRune(query, 0)
}

// Validate enforces the authenticated owner independently of the gateway.
func Validate(ctx context.Context, req *search.SearchReq, statuses ...string) *base.BaseResp {
	if req == nil || req.OwnerAgentId <= 0 || !ValidQuery(req.Query) || req.Cursor < 0 || req.Limit < 1 || req.Limit > 50 {
		return &base.BaseResp{Code: 400, Msg: "invalid search"}
	}
	if reqinfo.AuthFromContext(ctx).AgentID != req.OwnerAgentId {
		return &base.BaseResp{Code: 403, Msg: "owner identity mismatch"}
	}
	if req.Status != "" {
		valid := false
		for _, status := range statuses {
			if status == req.Status {
				valid = true
			}
		}
		if !valid {
			return &base.BaseResp{Code: 400, Msg: "invalid search status"}
		}
	}
	return nil
}

func Literal(text string) (string, int64) {
	id, _ := strconv.ParseInt(text, 10, 64)
	if id <= 0 || strconv.FormatInt(id, 10) != text {
		id = 0
	}
	return "%" + strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(text) + "%", id
}

type Row struct {
	Key            int64
	ID             int64
	Title          string
	Body           string
	Status         string
	UpdatedAt      int64
	ConversationID int64
	PeerID         int64
	ShortID        string
}

func Page(rows []Row, query string, limit int) *search.SearchResp {
	response := &search.SearchResp{Items: []*search.Record{}, HasMore: len(rows) > limit, BaseResp: &base.BaseResp{Code: 0, Msg: "success"}}
	if response.HasMore {
		rows = rows[:limit]
		response.NextCursor = rows[len(rows)-1].Key
	}
	for _, r := range rows {
		hit := &search.Record{Id: r.ID, Title: Preview(r.Title, query, 160), Preview: Preview(r.Body, query, 320), Status: r.Status, UpdatedAt: r.UpdatedAt}
		if r.ConversationID > 0 {
			v := r.ConversationID
			hit.ConversationId = &v
		}
		if r.PeerID > 0 {
			v := r.PeerID
			hit.PeerId = &v
		}
		if r.ShortID != "" {
			v := r.ShortID
			hit.ShortId = &v
		}
		response.Items = append(response.Items, hit)
	}
	return response
}

func Preview(content, query string, limit int) string {
	runes := []rune(content)
	if len(runes) <= limit {
		return content
	}
	start := 0
	lowerContent := strings.ToLower(content)
	if match := strings.Index(lowerContent, strings.ToLower(query)); match >= 0 {
		matchStart := utf8.RuneCountInString(lowerContent[:match])
		start = max(0, matchStart-(limit-utf8.RuneCountInString(query))/2)
		start = min(start, len(runes)-limit)
	}
	return string(runes[start : start+limit])
}
