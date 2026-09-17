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
