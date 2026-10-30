package dispatch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

const commissionTestAgent = "9007199254740995"

func commissionJournal(t *testing.T) *Journal {
	t.Helper()
	b := journalBinding(t)
	b.AgentID = commissionTestAgent
	b.Events = append(b.Events, "commission_order")
	return mustJournal(t, b)
}

func commissionNotification(order, version int64, role string, quotedIDs bool) json.RawMessage {
	id := func(value int64) any {
		if quotedIDs {
			return fmt.Sprint(value)
		}
		return value
	}
	raw, _ := json.Marshal(map[string]any{
		"notification_id": id(9223372036854775807), "source_type": "commission_order",
		"type": "order.state.changed.v1", "created_at": int64(1700000000000),
		"payload": map[string]any{
			"order_id": id(order), "order_version": version, "recipient_agent_id": id(9007199254740995),
			"recipient_role": role, "snapshot_id": id(9007199254740997),
			"occurred_at": int64(1700000000000), "to_state": "in_progress", "action": "untrusted_action",
		},
	})
	return raw
}

func mustAddCommission(t *testing.T, j *Journal, raw json.RawMessage) {
	t.Helper()
	if err := j.AddCommissionNotification(raw); err != nil {
		t.Fatal(err)
	}
}

func TestJournalCommissionDedupPreservesExactIDsAndPayload(t *testing.T) {
	j := commissionJournal(t)
	raw := commissionNotification(9007199254740993, 3, "seller", false)
	mustAddCommission(t, j, raw)
	wantData := append([]byte(nil), raw...)
	raw[0] = '['
	if !bytes.Equal(j.state.Jobs[0].Data, wantData) {
		t.Fatal("intake retained mutable caller memory")
	}
	duplicate := commissionNotification(9007199254740993, 3, "seller", true)
	duplicate = bytes.Replace(duplicate, []byte(`"9223372036854775807"`), []byte(`"41"`), 1)
	mustAddCommission(t, j, duplicate)
	if len(j.Snapshot()) != 1 {
		t.Fatal("different notification ID or ID encoding duplicated the same order fact")
	}
	for _, raw := range []json.RawMessage{
		commissionNotification(9007199254740994, 3, "seller", false),
		commissionNotification(9007199254740993, 4, "seller", false),
		commissionNotification(9007199254740993, 3, "buyer", false),
	} {
		mustAddCommission(t, j, raw)
	}
	if len(j.Snapshot()) != 4 {
		t.Fatal("distinct orders, versions or recipient roles were merged")
	}
	if _, ok, err := j.Next(); err != nil || ok {
		t.Fatalf("general worker claimed commission notification: ok=%v err=%v", ok, err)
	}
	j = mustJournal(t, j.binding)
	job, ok, err := j.NextKind("commission_order")
	if err != nil || !ok || job.Kind != "commission_order" || job.Message != nil || job.Scope != j.binding.Scope || job.Revision != j.binding.Revision || !bytes.Equal(job.Data, wantData) {
		t.Fatalf("bad persisted commission job: %+v ok=%v err=%v", job, ok, err)
	}
	job.Data[0] = '['
	if !bytes.Equal(j.state.Jobs[0].Data, wantData) {
		t.Fatal("claim returned mutable journal data")
	}
	for _, status := range j.Snapshot() {
		if status.Data != nil || status.Message != nil {
			t.Fatal("status exposed commission payload")
		}
	}
}

func TestJournalCommissionRejectsInvalidNotifications(t *testing.T) {
	j := commissionJournal(t)
	valid := commissionNotification(9007199254740993, 3, "seller", true)
	for _, test := range []struct {
		name, field, value string
		payload            bool
	}{
		{"source", "source_type", `"pm"`, false},
		{"notification missing", "notification_id", `null`, false},
		{"notification overflow", "notification_id", `9223372036854775808`, false},
		{"notification fraction", "notification_id", `1.1`, false},
		{"notification exponent", "notification_id", `1e3`, false},
		{"notification zero", "notification_id", `0`, false},
		{"notification whitespace", "notification_id", `" 41"`, false},
		{"payload null", "payload", `null`, false},
		{"recipient missing", "recipient_agent_id", `null`, true},
		{"recipient other", "recipient_agent_id", `9007199254740996`, true},
		{"order negative", "order_id", `-1`, true},
		{"order bool", "order_id", `true`, true},
		{"version zero", "order_version", `0`, true},
		{"version float", "order_version", `3.0`, true},
		{"snapshot missing", "snapshot_id", `null`, true},
		{"occurred missing", "occurred_at", `null`, true},
		{"role unknown", "recipient_role", `"observer"`, true},
		{"state blank", "to_state", `" "`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var notification, payload map[string]json.RawMessage
			_ = json.Unmarshal(valid, &notification)
			if test.payload {
				_ = json.Unmarshal(notification["payload"], &payload)
				payload[test.field] = json.RawMessage(test.value)
				notification["payload"], _ = json.Marshal(payload)
			} else {
				notification[test.field] = json.RawMessage(test.value)
			}
			raw, _ := json.Marshal(notification)
			if err := j.AddCommissionNotification(raw); err == nil {
				t.Fatal("invalid notification accepted")
			}
			if len(j.Snapshot()) != 0 {
				t.Fatal("invalid notification modified journal")
			}
		})
	}
	for _, raw := range [][]byte{
		[]byte("null"), []byte("[]"), append(append([]byte(nil), valid...), []byte(" {}")...),
		bytes.Replace(valid, []byte(`"recipient_role":"seller"`), []byte(`"recipient_role":"buyer","recipient_role":"seller"`), 1),
	} {
		if err := j.AddCommissionNotification(raw); err == nil {
			t.Fatalf("malformed or ambiguous JSON accepted: %s", raw)
		}
	}
	j.binding.Events = []string{"pm_push"}
	if err := j.AddCommissionNotification(valid); err == nil || len(j.Snapshot()) != 0 {
		t.Fatal("unsubscribed intake acknowledged without storing a job")
	}
}

func TestJournalCommissionPersistenceFailureAndRecovery(t *testing.T) {
	j := commissionJournal(t)
	raw := commissionNotification(9007199254740993, 3, "seller", false)
	path := j.path
	j.path = filepath.Join(t.TempDir(), "blocked", "journal.json")
	if err := os.WriteFile(filepath.Dir(j.path), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	blockedPath := j.path
	before := cloneJournal(j.state)
	if err := j.AddCommissionNotification(raw); err == nil || !reflect.DeepEqual(before, j.state) {
		t.Fatal("failed intake acknowledged or changed memory")
	}
	j.path = path
	mustAddCommission(t, j, raw)
	j.path = blockedPath
	before = cloneJournal(j.state)
	if _, ok, err := j.NextKind("commission_order"); err == nil || ok || !reflect.DeepEqual(before, j.state) {
		t.Fatal("failed claim advanced execution state")
	}
	j = mustJournal(t, j.binding)
	job, ok, err := j.NextKind("commission_order")
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	j = mustJournal(t, j.binding)
	if j.Snapshot()[0].Status != "unknown" || j.Retry(job.ID) == nil {
		t.Fatal("interrupted commission work was automatically retriable")
	}
	mustAddCommission(t, j, raw)
	if len(j.Snapshot()) != 1 {
		t.Fatal("recovered work duplicated")
	}
	if err := j.Reconcile(job.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	j = mustJournal(t, j.binding)
	if len(j.state.Jobs[0].Data) != 0 {
		t.Fatal("completed notification body was retained")
	}
	mustAddCommission(t, j, raw)
	if len(j.Snapshot()) != 1 {
		t.Fatal("compaction lost commission deduplication")
	}
}

func TestJournalCommissionReadRejectsIdentityAndKeyTampering(t *testing.T) {
	for _, field := range []string{"recipient_agent_id", "order_id", "order_version", "recipient_role"} {
		t.Run(field, func(t *testing.T) {
			j := commissionJournal(t)
			mustAddCommission(t, j, commissionNotification(9007199254740993, 3, "seller", false))
			var notification, payload map[string]json.RawMessage
			_ = json.Unmarshal(j.state.Jobs[0].Data, &notification)
			_ = json.Unmarshal(notification["payload"], &payload)
			payload[field] = json.RawMessage(`"42"`)
			if field == "recipient_role" {
				payload[field] = json.RawMessage(`"buyer"`)
			}
			notification["payload"], _ = json.Marshal(payload)
			j.state.Jobs[0].Data, _ = json.Marshal(notification)
			if err := WriteJSON(j.path, j.state); err != nil {
				t.Fatal(err)
			}
			if _, err := OpenJournal(j.binding); err == nil {
				t.Fatal("tampered commission identity accepted")
			}
		})
	}
}

func TestJournalCommissionCapacityAndExplicitWorkers(t *testing.T) {
	j := commissionJournal(t)
	first := commissionNotification(9007199254740993, 3, "seller", false)
	mustAddCommission(t, j, first)
	mustAddCommission(t, j, commissionNotification(9007199254740994, 3, "seller", false))
	messages := make([]Message, journalMaxPending-2)
	for i := range messages {
		messages[i] = Message{ID: fmt.Sprint(i), Conversation: "conversation", Sender: "peer", Receiver: commissionTestAgent, Content: "body"}
	}
	batch, _ := json.Marshal(map[string]any{"messages": messages})
	if err := j.AddMessages(batch); err != nil {
		t.Fatal(err)
	}
	if j.HasCapacity() {
		t.Fatal("capacity should be exhausted")
	}
	mustAddCommission(t, j, first)
	if err := j.AddCommissionNotification(commissionNotification(9007199254740995, 3, "seller", false)); err == nil {
		t.Fatal("capacity limit bypassed")
	}
	pm := mustNext(t, j)
	if pm.Kind != "pm_push" {
		t.Fatal("general worker received commission work")
	}
	commission, ok, err := j.NextKind("commission_order")
	if err != nil || !ok {
		t.Fatalf("PM blocked dedicated commission worker: %v %v", ok, err)
	}
	if _, ok, err := j.NextKind("commission_order"); err != nil || ok {
		t.Fatal("dedicated worker claimed two simultaneous commission jobs")
	}
	if err := j.Update(pm.ID, "no_reply", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if next := mustNext(t, j); next.Kind != "pm_push" {
		t.Fatal("commission work blocked general worker")
	}
	if err := j.Update(commission.ID, "completed", "verified", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := j.NextKind("commission_order"); err != nil || !ok {
		t.Fatal("next commission job unavailable after completion")
	}
	if _, ok, err := j.NextKind("unknown_action"); err == nil || ok {
		t.Fatal("unsupported event was executable")
	}
}
