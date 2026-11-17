package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/dispatch"
)

const noticeTestDeadline = int64(1795000020000)

func commissionNoticeDetailsFixture(t *testing.T, state string, credit bool) *accountWatch {
	t.Helper()
	return commissionNoticeDetailsFixtureWithLabels(t, state, credit, "冰雪奇缘之歌", "Sienna🐈", "测试卖家")
}

func commissionNoticeDetailsFixtureWithLabels(t *testing.T, state string, credit bool, service, buyer, seller string) *accountWatch {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("notice attempted a mutation: %s %s", r.Method, r.URL.Path)
			http.Error(out, "unexpected method", http.StatusMethodNotAllowed)
			return
		}
		switch r.URL.Path {
		case "/api/v1/orders/901":
			materialTestResponse(out, map[string]any{"order": map[string]any{
				"order_id": "901", "buyer_agent_id": "41", "seller_agent_id": "42", "version": 3, "state": state,
				"seller_confirm_due_at": noticeTestDeadline, "payment_due_at": noticeTestDeadline,
				"delivery_due_at": noticeTestDeadline, "buyer_confirm_due_at": noticeTestDeadline,
				"contract": map[string]any{"title": service, "currency": "CNY", "gross_amount_fen": 10, "seller_net_fen": 8},
			}})
		case "/api/v1/wallet/credits":
			if !credit {
				http.Error(out, "unavailable", http.StatusServiceUnavailable)
				return
			}
			if r.URL.Query().Get("cursor") == "" {
				materialTestResponse(out, map[string]any{"next_cursor": "second", "credits": []any{
					map[string]any{"order_id": "902", "amount_fen": 999, "currency": "CNY", "settlement_state": "withdrawn"},
				}})
				return
			}
			materialTestResponse(out, map[string]any{"credits": []any{
				map[string]any{"order_id": "901", "amount_fen": 8, "currency": "CNY", "available_at": noticeTestDeadline, "settlement_state": "unmatured"},
			}})
		case "/api/v2/public/agents/by-id/41/card":
			materialTestResponse(out, map[string]any{"card": map[string]string{"agent_name": buyer}})
		case "/api/v2/public/agents/by-id/42/card":
			materialTestResponse(out, map[string]any{"card": map[string]string{"agent_name": seller}})
		default:
			t.Errorf("unexpected notice read: %s", r.URL.Path)
			http.NotFound(out, r)
		}
	}))
	t.Cleanup(server.Close)
	return newDesktopServiceWatch(t, server.URL)
}

func commissionNoticeDetailsEvent(t *testing.T, state, event, command string) json.RawMessage {
	t.Helper()
	var fact desktopOrderFact
	if err := json.Unmarshal(commissionTestNotification(81, 901, "42"), &fact); err != nil {
		t.Fatal(err)
	}
	fact.Payload.OrderVersion = 3
	fact.Payload.RecipientRole = "seller"
	fact.Payload.ToState, fact.Payload.EventKind, fact.Payload.CommandKind = state, event, command
	raw, err := json.Marshal(fact)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestCommissionDesktopNoticeVerifiedDetails(t *testing.T) {
	deadline := time.UnixMilli(noticeTestDeadline).Local().Format("2006-01-02 15:04 -07:00")
	for _, tc := range []struct {
		name, state, event, command, locale string
		credit                              bool
		want, omit                          []string
	}{
		{"credited", "completed", "order.settlement.completed.v1", "", "zh", true, []string{"买家：Sienna", "净入账 ¥0.08", "结算期至 " + deadline}, []string{"¥9.99", "可在控制台查看余额或提现"}},
		{"credit fallback", "completed", "order.settlement.completed.v1", "", "en", false, []string{"Buyer: Sienna", "Net credited ¥0.08", "See the order for withdrawal availability"}, []string{deadline, "funds have matured"}},
		{"auto complete", "completed", "order.state.changed.v1", "buyer_confirmation_expired", "zh", false, []string{"确认超时", "已自动完成", "Sienna"}, []string{"净入账"}},
		{"payment timeout", "cancelled", "order.state.changed.v1", "payment_expired", "zh", false, []string{"付款超时", "Sienna"}, []string{"已退款"}},
		{"seller timeout", "cancelled", "order.state.changed.v1", "seller_expired", "zh", false, []string{"卖家接单超时", "未扣款"}, []string{"付款超时"}},
		{"buyer cancellation", "cancelled", "order.state.changed.v1", "cancel", "zh", false, []string{"买家已取消订单"}, []string{"已退款"}},
		{"refund pending", "refund_pending", "order.state.changed.v1", "delivery_expired", "en", false, []string{"refund in progress", "Refund amount ¥0.10"}, []string{"order refunded"}},
		{"refund completed", "cancelled", "order.refund.completed.v1", "late_payment_detected", "zh", false, []string{"款项已退还买方", "退款金额 ¥0.10"}, []string{"正在退款"}},
		{"waiting for seller", "awaiting_seller", "order.state.changed.v1", "", "zh", false, []string{"Sienna", "订单金额 ¥0.10", deadline}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := commissionNoticeDetailsFixture(t, tc.state, tc.credit)
			raw := commissionNoticeDetailsEvent(t, tc.state, tc.event, tc.command)
			queued, err := commissionDesktopMessage(raw, w.server.Endpoint, "42", tc.locale)
			if err != nil {
				t.Fatal(err)
			}
			message, err := w.resolveDesktopServiceName(context.Background(), queued)
			if err != nil {
				t.Fatal(err)
			}
			copy := message.Title + "\n" + message.Body
			for _, value := range tc.want {
				if !strings.Contains(copy, value) {
					t.Errorf("missing %q from %q", value, copy)
				}
			}
			for _, value := range tc.omit {
				if strings.Contains(copy, value) {
					t.Errorf("unexpected %q in %q", value, copy)
				}
			}
			target, _ := url.Parse(message.URL)
			if strings.Contains(copy, "{order_") || message.URL != queued.URL || target.Query().Get("order_id") != "901" || target.Query().Get("role") != "seller" {
				t.Fatalf("bad desktop routing or unresolved details: %+v", message)
			}
		})
	}
}

func TestCommissionMainNoticeDeliversVerifiedOrderContext(t *testing.T) {
	for _, tc := range []struct {
		state, event, command string
		want                  []string
	}{
		{"completed", "order.settlement.completed.v1", "", []string{`"amount_fen":8`, `"settlement_state":"unmatured"`, `"wallet_credit_available_at_local"`}},
		{"cancelled", "order.state.changed.v1", "payment_expired", []string{`"observed_command_kind":"payment_expired"`}},
		{"cancelled", "order.refund.completed.v1", "", []string{`"event_kind":"order.refund.completed.v1"`}},
		{"awaiting_buyer_confirmation", "order.reminder.buyer_confirmation_24h.v1", "", []string{`"order_deadline_at_local"`, `"event_kind":"order.reminder.buyer_confirmation_24h.v1"`}},
	} {
		t.Run(tc.event+"/"+tc.state, func(t *testing.T) {
			w := commissionNoticeDetailsFixture(t, tc.state, true)
			installCommissionIntakeRules(t, w)
			if err := w.enqueueCommissionMainNotice(commissionNoticeDetailsEvent(t, tc.state, tc.event, tc.command)); err != nil {
				t.Fatal(err)
			}
			queue, err := w.mainNoticeQueue()
			if err != nil {
				t.Fatal(err)
			}
			item, ok, err := queue.Next()
			if !ok || err != nil {
				t.Fatal(item, ok, err)
			}
			w.runMainNotice = func(_ context.Context, prompt string) (dispatch.Result, error) {
				_, payload, ok := strings.Cut(prompt, "\n\nUNTRUSTED COMMISSION EVENT:\n")
				if !ok {
					t.Fatal("missing hidden event boundary")
				}
				for _, value := range append(tc.want, `"counterparty_agent_name":"Sienna🐈"`, `"order_url":"`, "order_id=901", "role=seller") {
					if !strings.Contains(payload, value) {
						t.Errorf("missing %q from %q", value, payload)
					}
				}
				return dispatch.Result{Text: "Verified order notification"}, nil
			}
			if status, code := w.dispatchCommissionMainNotice(context.Background(), item); status != "delivered" {
				t.Fatalf("main notice %s: %s", status, code)
			}
		})
	}
}

func TestCommissionMacOSCardFitsPriorityFacts(t *testing.T) {
	for _, tc := range []struct {
		state, event, command, title, lastLine string
	}{
		{"completed", "order.state.changed.v1", "buyer_complete", "订单已完成", ""},
		{"validating", "order.state.changed.v1", "", "交付检查中", ""},
		{"in_progress", "order.state.changed.v1", "", "已付款 · 待交付", "交付截止 " + desktopCardTime(noticeTestDeadline)},
		{"awaiting_buyer_confirmation", "order.state.changed.v1", "", "待确认交付", "确认截止 " + desktopCardTime(noticeTestDeadline)},
		{"completed", "order.settlement.completed.v1", "", "净入账 ¥0.08", "可提现 " + desktopCardTime(noticeTestDeadline)},
		{"cancelled", "order.state.changed.v1", "seller_expired", "接单超时 · 订单关闭", "未扣款"},
		{"refund_pending", "order.state.changed.v1", "delivery_expired", "交付超时 · 退款中 · ¥0.10", ""},
		{"cancelled", "order.refund.completed.v1", "", "取消订单 · 退款已退回 · ¥0.10", ""},
		{"pending_payment", "order.state.changed.v1", "", "待付款 · ¥0.10", "付款截止 " + desktopCardTime(noticeTestDeadline)},
		{"awaiting_buyer_confirmation", "order.reminder.buyer_confirmation_24h.v1", "", "交付确认提醒", "确认截止 " + desktopCardTime(noticeTestDeadline)},
	} {
		t.Run(tc.title, func(t *testing.T) {
			w := commissionNoticeDetailsFixtureWithLabels(t, tc.state, true, "GPT 模型低成本体验", "Lynn's Agent", "凯瑞’s Codex")
			var fact desktopOrderFact
			if err := json.Unmarshal(commissionNoticeDetailsEvent(t, tc.state, tc.event, tc.command), &fact); err != nil {
				t.Fatal(err)
			}
			fact.Payload.PaymentDueAt = noticeTestDeadline
			fact.Payload.DeliveryDueAt = noticeTestDeadline
			fact.Payload.BuyerConfirmDue = noticeTestDeadline
			raw, err := json.Marshal(fact)
			if err != nil {
				t.Fatal(err)
			}
			queued, err := commissionDesktopMessageForPlatform(raw, w.server.Endpoint, "42", "zh", "darwin")
			if err != nil {
				t.Fatal(err)
			}
			message, err := w.resolveDesktopServiceName(context.Background(), queued)
			if err != nil {
				t.Fatal(err)
			}
			body := "买家：Lynn's Agent · 当前：凯瑞’s Codex\nGPT 模型低成本体验"
			if tc.lastLine != "" {
				body += "\n" + tc.lastLine
			}
			if message.Title != tc.title || message.Body != body || message.URL != queued.URL {
				t.Fatalf("wrong macOS card: %+v, want %q and %q", message, tc.title, body)
			}
		})
	}
}

func TestCommissionDesktopLongLabelsStayInPayload(t *testing.T) {
	service := strings.Repeat("数据备份与恢复", 9)
	account := strings.Repeat("Team Alpha ", 17) + "Lead"
	w := commissionNoticeDetailsFixtureWithLabels(t, "completed", true, service, "Lynn's Agent", account)
	for _, platform := range []string{"darwin", "windows"} {
		t.Run(platform, func(t *testing.T) {
			raw := commissionNoticeDetailsEvent(t, "completed", "order.state.changed.v1", "buyer_complete")
			queued, err := commissionDesktopMessageForPlatform(raw, w.server.Endpoint, "42", "zh", platform)
			if err != nil {
				t.Fatal(err)
			}
			got, err := w.resolveDesktopServiceName(context.Background(), queued)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(got.Body, service) || !strings.Contains(got.Body, account) ||
				strings.Contains(got.Title+got.Body, "…") || got.URL != queued.URL {
				t.Fatalf("full labels lost or order target changed: %+v", got)
			}
		})
	}
}

func TestCommissionMaterialCardDoesNotClipQuestionOrFile(t *testing.T) {
	question := "请补充用于版本恢复演练的详细备份目录说明和可公开的示例"
	file := "inputs/backup-and-recovery-implementation-notes.md"
	_, body := buyerMaterialDesktopCard("zh", &commissionInputRequest{
		Question: question, MissingInputs: []string{file},
	})
	if !strings.Contains(body, question) || !strings.Contains(body, file) || strings.Contains(body, "…") {
		t.Fatal(body)
	}
}
