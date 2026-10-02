package tradebff

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDashboardSearchDelegationAndVersionFence(t *testing.T) {
	version := `,"search_version":1`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") != "合同_%!" || r.URL.Query().Get("counterparty_ids") != "72,83" || r.URL.Query().Get("state") != "completed" {
			t.Errorf("query %s", r.URL.RawQuery)
		}
		claims := tokenClaims(t, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if claims.Subject != "42" || claims.Scope != "orders:read" {
			t.Errorf("claims %#v", claims)
		}
		io.WriteString(w, `{"code":0,"data":{"items":[{"order_id":"9007199254740993","state":"completed","matched_preview":"合同_%!","contract":{"title":"合同"},"counterparty":{"agent_id":"72","display_name":"Peer"}}],"page":{"next_cursor":"9"}`+version+`}}`)
	}))
	defer server.Close()
	s := configuredService(t, server.URL)
	g, err := s.SearchDashboard(context.Background(), 42, "order", "合同_%!", "completed", "", 2, []int64{72, 83})
	if err != nil || len(g.Items) != 1 || g.Items[0].ID != "9007199254740993" || !g.HasMore || g.NextCursor != "9" {
		t.Fatalf("%#v %v", g, err)
	}
	version = ""
	if _, err = s.SearchDashboard(context.Background(), 42, "order", "合同_%!", "completed", "", 2, []int64{72, 83}); err == nil {
		t.Fatal("old server must fail closed")
	}
}
