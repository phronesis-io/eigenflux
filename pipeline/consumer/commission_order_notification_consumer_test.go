package consumer

import (
	"encoding/json"
	"fmt"
	"strconv"
	"testing"
)

func TestParseCommissionWorkflowResultsPreservesIndependentSameVersionFacts(t *testing.T) {
	const orderID int64 = 9007199254740995
	const recipientID int64 = 9007199254740997
	cases := []struct {
		kind, state, role string
	}{
		{"order.refund.processing_error.v1", "refund_pending", "buyer"},
		{"order.refund.completed.v1", "cancelled", "buyer"},
		{"order.settlement.processing_error.v1", "completed", "seller"},
		{"order.settlement.completed.v1", "completed", "seller"},
		{"order.validation.processing_error.v1", "validating", "seller"},
	}
	seen := make(map[string]bool)
	for index, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			values := validCommissionNotificationValues(t)
			id := orderID + 10 + int64(index)
			payload := commissionOrderNotificationPayload{
				EventID: id, EventKind: tc.kind, OrderID: orderID, OrderVersion: 8,
				RecipientAgentID: recipientID, RecipientRole: tc.role,
				State: tc.state, FromState: tc.state, ToState: tc.state,
				ActorKind: "system", SnapshotID: orderID + 2, OccurredAt: 1700000000000,
			}
			encoded, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			values["event_id"], values["outbox_id"] = strconv.FormatInt(id, 10), strconv.FormatInt(id, 10)
			values["order_id"], values["recipient_agent_id"] = strconv.FormatInt(orderID, 10), strconv.FormatInt(recipientID, 10)
			values["order_version"], values["occurred_at"] = "8", "1700000000000"
			values["event_kind"], values["payload_json"] = tc.kind, string(encoded)
			values["dedupe_key"] = fmt.Sprintf("order:%d:intent:71:event:%s:recipient:%d", orderID, tc.kind, recipientID)
			event, parsed, err := parseCommissionOrderNotification(values)
			if err != nil {
				t.Fatalf("workflow result rejected by existing schema: %v", err)
			}
			if event.OrderID != orderID || event.RecipientAgentID != recipientID || event.OutboxID != id ||
				event.OrderVersion != 8 || parsed != payload || seen[event.DedupeKey] {
				t.Fatalf("workflow result lost its identity: event=%#v payload=%#v", event, parsed)
			}
			seen[event.DedupeKey] = true
			values["recipient_agent_id"] = strconv.FormatInt(recipientID+1, 10)
			if _, _, err := parseCommissionOrderNotification(values); err == nil {
				t.Fatal("workflow result accepted another recipient")
			}
		})
	}
}

func validCommissionNotificationValues(t *testing.T) map[string]any {
	t.Helper()
	payload, err := json.Marshal(commissionOrderNotificationPayload{
		EventID: 10, EventKind: "order.state.changed.v1", OrderID: 20, OrderVersion: 3,
		RecipientAgentID: 30, RecipientRole: "buyer", State: "in_progress", ToState: "in_progress",
		CommandKind: "accept", ActorKind: "seller", ActorAgentID: 40, SnapshotID: 50, OccurredAt: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{
		"event_id": "10", "outbox_id": "11", "schema_version": "1", "event_kind": "order.state.changed.v1",
		"dedupe_key": "order:20:version:3:recipient:30:event:order.state.changed.v1", "order_id": "20",
		"order_version": "3", "recipient_agent_id": "30", "occurred_at": "60", "payload_json": string(payload),
	}
}

func TestParseCommissionOrderNotificationValidatesEnvelopeAndRecipient(t *testing.T) {
	values := validCommissionNotificationValues(t)
	event, payload, err := parseCommissionOrderNotification(values)
	if err != nil || event.OutboxID != 11 || payload.RecipientRole != "buyer" {
		t.Fatalf("parsed=(%#v,%#v,%v)", event, payload, err)
	}
	for name, mutate := range map[string]func(map[string]any){
		"unsupported schema": func(values map[string]any) { values["schema_version"] = "2" },
		"payload mismatch":   func(values map[string]any) { values["recipient_agent_id"] = "31" },
		"dedupe mismatch":    func(values map[string]any) { values["dedupe_key"] = "order:20:version:3:recipient:31" },
		"dedupe prefix mismatch": func(values map[string]any) {
			values["dedupe_key"] = "order:20:version:3:recipient:300:event:order.state.changed.v1"
		},
		"oversized payload": func(values map[string]any) { values["payload_json"] = strconv.Quote(string(make([]byte, 17<<10))) },
	} {
		t.Run(name, func(t *testing.T) {
			copyValues := validCommissionNotificationValues(t)
			mutate(copyValues)
			if _, _, err := parseCommissionOrderNotification(copyValues); err == nil {
				t.Fatal("invalid notification was accepted")
			}
		})
	}
}

func TestParseCommissionOrderNotificationAllowsReminderWithoutCommand(t *testing.T) {
	values := validCommissionNotificationValues(t)
	values["event_kind"] = "order.reminder.seller_confirmation.v1"
	values["dedupe_key"] = "order:20:reminder:seller_confirmation:70:recipient:30"
	payload := commissionOrderNotificationPayload{
		EventID: 10, EventKind: "order.reminder.seller_confirmation.v1", OrderID: 20, OrderVersion: 3,
		RecipientAgentID: 30, RecipientRole: "seller", State: "awaiting_seller", FromState: "awaiting_seller",
		ToState: "awaiting_seller", ActorKind: "system", SnapshotID: 50, OccurredAt: 60, SellerConfirmDueAt: 70,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	values["payload_json"] = string(encoded)

	if _, parsed, err := parseCommissionOrderNotification(values); err != nil || parsed.CommandKind != "" {
		t.Fatalf("reminder parsed=(%#v,%v)", parsed, err)
	}
}

func TestParseCommissionOrderNotificationRequiresCommandForStateChange(t *testing.T) {
	values := validCommissionNotificationValues(t)
	var payload commissionOrderNotificationPayload
	if err := json.Unmarshal([]byte(values["payload_json"].(string)), &payload); err != nil {
		t.Fatal(err)
	}
	payload.CommandKind = ""
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	values["payload_json"] = string(encoded)

	if _, _, err := parseCommissionOrderNotification(values); err == nil {
		t.Fatal("state-change notification without command_kind was accepted")
	}
}

func TestParseCommissionOrderNotificationRejectsInconsistentStateAndActor(t *testing.T) {
	values := validCommissionNotificationValues(t)
	var payload commissionOrderNotificationPayload
	if err := json.Unmarshal([]byte(values["payload_json"].(string)), &payload); err != nil {
		t.Fatal(err)
	}
	payload.ToState = "completed"
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	values["payload_json"] = string(encoded)
	if _, _, err := parseCommissionOrderNotification(values); err == nil {
		t.Fatal("inconsistent state payload was accepted")
	}

	values = validCommissionNotificationValues(t)
	if err := json.Unmarshal([]byte(values["payload_json"].(string)), &payload); err != nil {
		t.Fatal(err)
	}
	payload.ActorKind = "system"
	payload.ActorAgentID = 41
	encoded, err = json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	values["payload_json"] = string(encoded)
	if _, _, err := parseCommissionOrderNotification(values); err == nil {
		t.Fatal("system actor with an Agent ID was accepted")
	}
}
