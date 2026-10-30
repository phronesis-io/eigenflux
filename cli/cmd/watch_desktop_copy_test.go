package cmd

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/desktopnotify"
)

// The fixture is the approved document's revised columns, including role labels.
// Exercise the public rendering path so state/event routing cannot drift from copy.
func TestDesktopCopyMatchesRevisedDocument(t *testing.T) {
	raw, err := os.ReadFile("testdata/desktop_notification_copy_20261013.json")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]map[string]string
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ section, row, state, kind, command, local string }{
		{"交易进展", "2", "awaiting_seller", "", "", ""},
		{"交易进展", "3", "pending_payment", "", "", ""},
		{"交易进展", "4", "in_progress", "", "", ""},
		{"交易进展", "5", "validating", "", "", ""},
		{"交易进展", "6", "awaiting_buyer_confirmation", "", "", ""},
		{"交易进展", "7", "awaiting_buyer_confirmation", "order.reminder.buyer_confirmation_24h.v1", "", ""},
		{"交易进展", "8", "completed", "", "", ""},
		{"交易进展", "9", "cancelled", "", "reject_order", ""},
		{"交易进展", "10", "cancelled", "", "", ""},
		{"交易进展", "11", "refund_pending", "", "", ""},
		{"交易进展", "12", "refunded", "", "", ""},
		{"处理结果", "2", "refund_pending", "order.refund.processing_error.v1", "", ""},
		{"处理结果", "3", "cancelled", "order.refund.completed.v1", "", ""},
		{"处理结果", "4", "completed", "order.settlement.processing_error.v1", "", ""},
		{"处理结果", "5", "completed", "order.settlement.completed.v1", "", ""},
		{"处理结果", "6", "validating", "order.validation.processing_error.v1", "", ""},
		{"本机处理异常", "2", "", "", "", "needs_user"},
		{"本机处理异常", "3", "", "", "", "failed"},
		{"本机处理异常", "4", "", "", "", "unknown"},
		{"通知测试", "2", "", "", "", "test"},
		{"兼容及兜底", "2", "preparing_materials", "", "", ""},
		{"兼容及兜底", "3", "delivered", "", "", ""},
		{"兼容及兜底", "4", "rejected", "", "", ""},
		{"兼容及兜底", "5", "expired", "", "", ""},
		{"兼容及兜底", "6", "future_state", "", "", ""},
	}
	previousZone := time.Local
	time.Local = time.FixedZone("fixture", 8*60*60)
	t.Cleanup(func() { time.Local = previousZone })
	payment := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC).UnixMilli()
	delivery, confirm := payment+86400000, payment+2*86400000
	for _, tc := range cases {
		for _, language := range []string{"zh", "en"} {
			for _, role := range []string{"buyer", "seller"} {
				t.Run(tc.section+tc.row+"/"+language+"/"+role, func(t *testing.T) {
					column, buyer, seller := "F", "Buyer sees:\n", "Seller sees:\n"
					if language == "zh" {
						column, buyer, seller = "D", "买方收到：\n", "卖方收到：\n"
					}
					want := document[tc.section][column+tc.row]
					if strings.HasPrefix(want, buyer) {
						parts := strings.Split(strings.TrimPrefix(want, buyer), "\n\n"+seller)
						if len(parts) != 2 {
							t.Fatal("invalid role copy fixture")
						}
						want = parts[0]
						if role == "seller" {
							want = parts[1]
						}
					}
					want = strings.NewReplacer(
						"{服务名}", "{service_name}",
						"{付款截止时间}", "2026-10-13 20:00 +08:00", "{payment_due}", "2026-10-13 20:00 +08:00",
						"{交付截止时间}", "2026-10-14 20:00 +08:00", "{delivery_due}", "2026-10-14 20:00 +08:00",
						"{确认截止时间}", "2026-10-15 20:00 +08:00", "{confirm_due}", "2026-10-15 20:00 +08:00",
					).Replace(want)
					var message desktopnotify.Message
					switch tc.local {
					case "test":
						message = notificationTestMessage(language, "test:1", "https://example.com/page")
					case "needs_user", "failed", "unknown":
						message.Title, message.Body = desktopBlockerText(language, tc.local)
						message.ID, message.URL = "local:1", "https://example.com/page"
					default:
						var fact desktopOrderFact
						if err := json.Unmarshal(commissionTestNotification(81, 901, "42"), &fact); err != nil {
							t.Fatal(err)
						}
						fact.Payload.ToState, fact.Payload.EventKind, fact.Payload.CommandKind, fact.Payload.RecipientRole = tc.state, tc.kind, tc.command, role
						fact.Payload.PaymentDueAt, fact.Payload.DeliveryDueAt, fact.Payload.BuyerConfirmDue = payment, delivery, confirm
						data, _ := json.Marshal(fact)
						message, err = commissionDesktopMessage(data, "https://example.com", "42", language)
						if err != nil {
							t.Fatal(err)
						}
					}
					if got := message.Title + "\n" + message.Body; got != want {
						t.Fatalf("document mismatch:\n got %q\nwant %q", got, want)
					}
					if err := desktopnotify.Validate(message); err != nil {
						t.Fatal(err)
					}
				})
			}
		}
	}
}

func TestDesktopCopyEventPrecedenceAndAliases(t *testing.T) {
	for _, tt := range []struct{ state, kind, envelope, command, want string }{
		{"cancelled", "order.refund.processing_error.v1", "order.refund.completed.v1", "reject_order", "refund hit a problem"},
		{"in_progress", "future.event", "order.refund.completed.v1", "", "cancelled, payment returned"},
		{"in_progress", "future.event", "order.reminder.buyer_confirmation_24h.v1", "", "still needs your confirmation"},
		{"awaiting_payment", "", "order.state.changed.v1", "", "accepted, payment needed"},
		{"awaiting_materials", "", "order.state.changed.v1", "", "order is being updated"},
		{"refunding", "", "order.state.changed.v1", "", "refund started"},
	} {
		title, body := commissionDesktopCopy("en", tt.envelope, orderNotificationPayload{ToState: tt.state, EventKind: tt.kind, CommandKind: tt.command, RecipientRole: "buyer"})
		if !strings.Contains(title, tt.want) || strings.Contains(body, "1970") || strings.Contains(body, "{confirm_due}") || strings.Contains(body, "{payment_due}") {
			t.Fatalf("%+v: %s / %s", tt, title, body)
		}
	}
}

func TestDesktopCopyMissingDeadlinesDoNotInventTime(t *testing.T) {
	for _, language := range []string{"zh", "en", "fr"} {
		for _, state := range []string{"pending_payment", "in_progress", "awaiting_buyer_confirmation"} {
			for _, missing := range []int64{0, -1} {
				_, body := commissionDesktopCopy(language, "order.state.changed.v1", orderNotificationPayload{RecipientRole: "buyer", ToState: state, PaymentDueAt: missing, DeliveryDueAt: missing, BuyerConfirmDue: missing})
				fallback := "the deadline shown on the order page"
				if language == "zh" {
					fallback = "订单页所示时间"
				}
				if !strings.Contains(body, fallback) || strings.Contains(body, "1970") || strings.Contains(body, "{") {
					t.Fatal(body)
				}
			}
		}
	}
}
