package tradebff

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
)

func TestTradeDeliveryReviewPassThroughPreservesFieldsAndSnapshotPrecision(t *testing.T) {
	for _, status := range []string{"reviewing", "changes_requested", "manual_review", "passed", "failed", "closed"} {
		for _, list := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/list=%v", status, list), func(t *testing.T) {
				review := fmt.Sprintf(`{"submission_number":1,"status":%q,"reason":"Revise the contracted conclusion","snapshot_id":9007199254740993,"rework_due_at":1795600000000}`, status)
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					data := `{"order_id":"9007199254740993","delivery_review":` + review + `}`
					if list {
						data = `{"items":[` + data + `]}`
					}
					fmt.Fprint(w, `{"code":0,"data":`+data+`}`)
				}))
				defer upstream.Close()
				service := configuredService(t, upstream.URL)
				c := orderAccountContext("42", 42)
				if list {
					c = app.NewContext(0)
					c.Set("agent_id", int64(42))
					service.TradeOrders(context.Background(), c)
				} else {
					service.TradeOrder(context.Background(), c)
				}
				if c.Response.StatusCode() != http.StatusOK {
					t.Fatalf("status=%d body=%s", c.Response.StatusCode(), c.Response.Body())
				}
				var response struct {
					Data json.RawMessage `json:"data"`
				}
				if err := json.Unmarshal(c.Response.Body(), &response); err != nil {
					t.Fatal(err)
				}
				var order struct {
					Review json.RawMessage `json:"delivery_review"`
				}
				if list {
					var page struct {
						Items []json.RawMessage `json:"items"`
					}
					if json.Unmarshal(response.Data, &page) != nil || len(page.Items) != 1 {
						t.Fatal("lost list item")
					}
					if err := json.Unmarshal(page.Items[0], &order); err != nil {
						t.Fatal(err)
					}
				} else if err := json.Unmarshal(response.Data, &order); err != nil {
					t.Fatal(err)
				}
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(order.Review, &fields); err != nil || len(fields) != 5 || string(fields["snapshot_id"]) != "9007199254740993" || string(fields["status"]) != fmt.Sprintf("%q", status) {
					t.Fatalf("review lost: %s %v", order.Review, err)
				}
			})
		}
	}
}
