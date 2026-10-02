package tradebff

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"eigenflux_server/pkg/dashboardsearch"
)

func (s *Service) SearchDashboard(ctx context.Context, owner int64, kind, query, status, cursor string, limit int, peers []int64) (dashboardsearch.Group, error) {
	group := dashboardsearch.Group{Type: kind, Items: []dashboardsearch.Result{}}
	values := url.Values{"q": {query}, "cursor": {cursor}, "limit": {strconv.Itoa(limit)}}
	scope, operation, path := "commissions:mine:read", "console.trade.commissions.list", "/api/v2/console/trade/commissions"
	if kind == "service" {
		values.Set("status", status)
	} else if kind == "order" {
		scope, operation, path = "orders:read", "console.trade.orders.list", "/api/v2/console/trade/orders"
		values.Set("state", status)
		parts := make([]string, len(peers))
		for i, id := range peers {
			parts[i] = strconv.FormatInt(id, 10)
		}
		if len(parts) > 0 {
			values.Set("counterparty_ids", strings.Join(parts, ","))
		}
	} else {
		return group, fmt.Errorf("unsupported search type")
	}
	raw, err := s.fetch(ctx, owner, scope, operation, http.MethodGet, path, values, nil, "", false)
	if err != nil {
		return group, err
	}
	type content struct {
		Title       string `json:"title"`
		Description string `json:"capability_description"`
	}
	var page struct {
		SearchVersion int `json:"search_version"`
		Items         []struct {
			CommissionID string   `json:"commission_id"`
			OrderID      string   `json:"order_id"`
			Draft        *content `json:"draft"`
			Public       *content `json:"public"`
			Contract     content  `json:"contract"`
			Counterparty struct {
				ID   string `json:"agent_id"`
				Name string `json:"display_name"`
			} `json:"counterparty"`
			Status    string `json:"status"`
			State     string `json:"state"`
			Role      string `json:"role"`
			Preview   string `json:"matched_preview"`
			UpdatedAt int64  `json:"updated_at"`
		} `json:"items"`
		Page struct {
			NextCursor *string `json:"next_cursor"`
		} `json:"page"`
	}
	if err = json.Unmarshal(raw, &page); err != nil {
		return group, err
	}
	// Older Commission versions ignore q. Never present their unfiltered list as search results.
	if page.SearchVersion != 1 {
		return group, fmt.Errorf("Commission search is not available")
	}
	if page.Page.NextCursor != nil {
		group.NextCursor = *page.Page.NextCursor
		group.HasMore = group.NextCursor != ""
	}
	for _, item := range page.Items {
		r := dashboardsearch.Result{ID: item.CommissionID, Status: item.Status, Preview: item.Preview, UpdatedAt: item.UpdatedAt}
		if kind == "service" {
			c := item.Public
			if c == nil {
				c = item.Draft
			}
			if c != nil {
				r.Title = c.Title
			}
			r.URL = "/dashboard/search?" + url.Values{"q": {r.ID}, "type": {"service"}, "detail": {r.ID}}.Encode()
		} else {
			r.ID = item.OrderID
			r.Title = item.Contract.Title
			r.Status = item.State
			r.PeerID = item.Counterparty.ID
			if r.Preview == "" {
				r.Preview = item.Counterparty.Name
			}
			r.URL = "/dashboard/search?" + url.Values{"q": {r.ID}, "type": {"order"}, "detail": {r.ID}}.Encode()
		}
		group.Items = append(group.Items, r)
	}
	return group, nil
}
