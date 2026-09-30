package tradebff

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/route/param"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestCommissionReviewsDelegatesSessionAndPreservesPagination(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RequestURI() != "/api/v1/commissions/42/reviews?cursor=next%2Bpage&limit=20" {
			t.Errorf("unexpected URI: %s", r.URL.RequestURI())
		}
		claims := tokenClaims(t, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if claims.Subject != "7" || claims.Scope != "commissions:reviews:read" || claims.Operation != "console.trade.commissions.reviews.list" || claims.Method != http.MethodGet {
			t.Errorf("unexpected claims: %#v", claims)
		}
		io.WriteString(w, `{"code":0,"data":{"reviews":[{"review_id":"9007199254740993"}],"next_cursor":"last-page"}}`)
	}))
	defer server.Close()
	ctx := app.NewContext(0)
	ctx.Params = param.Params{{Key: "commission_id", Value: "42"}}
	ctx.Set("agent_id", int64(7))
	ctx.Request.SetRequestURI("/api/v2/console/bff/trade/commissions/42/reviews?cursor=next%2Bpage&limit=20&agent_id=99")
	configuredService(t, server.URL).TradeCommissionReviews(context.Background(), ctx)
	if ctx.Response.StatusCode() != http.StatusOK || !strings.Contains(string(ctx.Response.Body()), `"next_cursor":"last-page"`) || strings.Contains(string(ctx.Response.Body()), `"total"`) {
		t.Fatalf("status=%d body=%s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
}

func TestCommissionReviewsEnrichesBuyerWithCurrentPublicIdentity(t *testing.T) {
	database, err := gorm.Open(sqlite.Open("file:trade-review-identities?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Exec(`CREATE TABLE agents (agent_id INTEGER PRIMARY KEY, short_id TEXT, agent_name TEXT NOT NULL, agent_name_en TEXT NOT NULL DEFAULT '', identity_state TEXT NOT NULL DEFAULT 'active')`).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Exec(`INSERT INTO agents (agent_id, short_id, agent_name, agent_name_en) VALUES (11, 'NoVaA', 'Nova 研究助手', 'Nova Research Agent')`).Error; err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"code":0,"data":{"reviews":[{"review_id":"91","buyer_agent_id":"11","score":4,"text":"clear"}],"next_cursor":""}}`)
	}))
	defer server.Close()
	service := configuredService(t, server.URL)
	service.db = database
	ctx := app.NewContext(0)
	ctx.Params = param.Params{{Key: "commission_id", Value: "42"}}
	ctx.Set("agent_id", int64(7))
	service.TradeCommissionReviews(context.Background(), ctx)
	body := string(ctx.Response.Body())
	for _, expected := range []string{`"display_name":"Nova 研究助手"`, `"display_name_en":"Nova Research Agent"`, `"short_id":"NoVaA"`} {
		if ctx.Response.StatusCode() != http.StatusOK || !strings.Contains(body, expected) {
			t.Fatalf("status=%d missing=%s body=%s", ctx.Response.StatusCode(), expected, body)
		}
	}
}

func TestCommissionReviewsRejectsInvalidIDsAndMissingSession(t *testing.T) {
	for _, id := range []string{"", "0", "-1", "01", "+1", "9223372036854775808", "1/reviews"} {
		ctx := app.NewContext(0)
		ctx.Params = param.Params{{Key: "commission_id", Value: id}}
		NewUnavailable("").TradeCommissionReviews(context.Background(), ctx)
		if ctx.Response.StatusCode() != http.StatusBadRequest {
			t.Errorf("id=%q status=%d", id, ctx.Response.StatusCode())
		}
	}
	ctx := app.NewContext(0)
	ctx.Params = param.Params{{Key: "commission_id", Value: "42"}}
	NewUnavailable("").TradeCommissionReviews(context.Background(), ctx)
	if ctx.Response.StatusCode() != http.StatusUnauthorized {
		t.Fatalf("status=%d", ctx.Response.StatusCode())
	}
	ctx.Set("agent_id", int64(7))
	NewUnavailable("").TradeCommissionReviews(context.Background(), ctx)
	if ctx.Response.StatusCode() != http.StatusServiceUnavailable {
		t.Fatalf("status=%d", ctx.Response.StatusCode())
	}
}
