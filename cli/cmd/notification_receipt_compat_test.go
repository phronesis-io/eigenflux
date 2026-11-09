package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// An old gateway ignores headers but strictly decodes its original ACK body.
// Two pages expose regressions where the first page prevents later intake.
func TestNewCLIOrderIntakeAgainstStrictOldGateway(t *testing.T) {
	for _, desktopOnly := range []bool{false, true} {
		t.Run(fmt.Sprint("notifications_only=", desktopOnly), func(t *testing.T) {
			pending := map[string]bool{}
			for i := 0; i < 51; i++ {
				pending[fmt.Sprint(1000+i)] = true
			}
			reads, acks := 0, 0
			gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v2/notifications/pending":
					reads++
					start, end, more := 0, 50, true
					if r.URL.Query().Get("cursor") == "next" {
						start, end, more = 50, 51, false
						if acks != 50 {
							t.Error("missing first-page ACK")
						}
					}
					rows := []json.RawMessage{}
					for i := start; i < end; i++ {
						if pending[fmt.Sprint(1000+i)] {
							rows = append(rows, commissionTestNotification(int64(1000+i), int64(2000+i), "42"))
						}
					}
					_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 0, "data": map[string]interface{}{"notifications": rows, "has_more": more, "next_cursor": "next"}})
				case "/api/v2/notifications/ack":
					var old struct {
						Notifications []struct {
							ID     string `json:"notification_id"`
							Source string `json:"source_type"`
						} `json:"notifications"`
					}
					decoder := json.NewDecoder(r.Body)
					decoder.DisallowUnknownFields()
					if err := decoder.Decode(&old); err != nil {
						t.Errorf("old schema rejected new CLI: %v", err)
						http.Error(w, "unknown field", 400)
						return
					}
					if len(old.Notifications) > 50 {
						t.Error("ACK batch limit changed")
					}
					for _, item := range old.Notifications {
						if item.Source != "commission_order" {
							t.Error("wrong source")
						}
						delete(pending, item.ID)
						acks++
					}
					_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 0, "data": map[string]interface{}{"acknowledged": len(old.Notifications)}})
				default:
					t.Errorf("unexpected endpoint: %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer gateway.Close()
			var watch *accountWatch
			if desktopOnly {
				watch = newNotificationWatch(t, gateway.URL)
			} else {
				watch = newCommissionWatch(t, gateway.URL, "commission_order")
			}
			if err := watch.pollCommissionNotifications(context.Background()); err != nil {
				t.Fatal(err)
			}
			if reads != 2 || acks != 51 || len(pending) != 0 {
				t.Fatalf("intake stalled: reads=%d ACKs=%d pending=%d", reads, acks, len(pending))
			}
			if desktopOnly {
				if n, _ := watch.desktop.Counts(); n != 51 {
					t.Fatalf("lost desktop notices: %d", n)
				}
			} else if n := len(watch.journal.Snapshot()); n != 51 {
				t.Fatalf("lost execution jobs: %d", n)
			}
		})
	}
}
