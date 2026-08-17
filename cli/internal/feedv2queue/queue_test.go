package feedv2queue

import (
	"encoding/json"
	"errors"
	"testing"
)

func payload(batch string, epoch int64, token string, cardVersion int64) json.RawMessage {
	encoded, _ := json.Marshal(map[string]interface{}{
		"batch_id": batch,
		"lease":    map[string]interface{}{"epoch": epoch, "token": token, "expires_at": int64(1000)},
		"agent_card_updates": map[string]interface{}{
			"42": map[string]interface{}{"public_card_version": cardVersion},
		},
	})
	return encoded
}

func TestQueueRenewsAndBoundsStaleEntries(t *testing.T) {
	queue := New(t.TempDir())
	if _, err := queue.Enqueue(payload("renew-me", 3, "proof", 1)); err != nil {
		t.Fatal(err)
	}
	renewed, err := queue.Renew("renew-me", func(entry Entry) (int64, error) {
		if entry.LeaseEpoch != 3 || entry.LeaseToken != "proof" {
			t.Fatalf("renew used stale proof: %#v", entry)
		}
		return 9000, nil
	})
	if err != nil || renewed.LeaseUntil != 9000 || !json.Valid(renewed.Payload) {
		t.Fatalf("renewed=%#v err=%v", renewed, err)
	}
	if _, err := queue.MoveToStale("renew-me", "LEASE_FENCED"); err != nil {
		t.Fatal(err)
	}
	entries, _, _ := queue.Snapshot()
	stale, err := queue.StaleSnapshot()
	if err != nil || len(entries) != 0 || len(stale) != 1 || stale[0].StaleReason != "LEASE_FENCED" {
		t.Fatalf("entries=%#v stale=%#v err=%v", entries, stale, err)
	}
	_, versions, err := queue.Snapshot()
	if err != nil || versions["42"] != 0 {
		t.Fatalf("stale batch advanced card versions: %#v err=%v", versions, err)
	}
}

func TestQueuePersistsReplacesLeaseAndAcknowledges(t *testing.T) {
	queue := New(t.TempDir())
	depth, err := queue.Enqueue(payload("1", 1, "old", 7))
	if err != nil || depth != 1 {
		t.Fatalf("enqueue depth=%d err=%v", depth, err)
	}
	depth, err = queue.Enqueue(payload("1", 2, "new", 8))
	if err != nil || depth != 1 {
		t.Fatalf("replace depth=%d err=%v", depth, err)
	}
	entries, versions, err := queue.Snapshot()
	if err != nil || len(entries) != 1 || entries[0].LeaseEpoch != 2 || entries[0].LeaseToken != "new" || versions["42"] != 0 {
		t.Fatalf("snapshot entries=%#v versions=%#v err=%v", entries, versions, err)
	}
	remaining, err := queue.Acknowledge("1", func(Entry) error { return errors.New("network") })
	if err == nil || remaining != 0 {
		t.Fatalf("failed ack result remaining=%d err=%v", remaining, err)
	}
	entries, _, _ = queue.Snapshot()
	if len(entries) != 1 {
		t.Fatal("failed network ack removed durable entry")
	}
	remaining, err = queue.Acknowledge("1", func(entry Entry) error {
		if entry.LeaseEpoch != 2 {
			t.Fatalf("pushed stale lease: %#v", entry)
		}
		return nil
	})
	if err != nil || remaining != 0 {
		t.Fatalf("successful ack remaining=%d err=%v", remaining, err)
	}
	_, versions, err = queue.Snapshot()
	if err != nil || versions["42"] != 8 {
		t.Fatalf("ack did not commit card version: versions=%#v err=%v", versions, err)
	}
}

func TestQueueEnforcesSafetyLimit(t *testing.T) {
	queue := New(t.TempDir())
	for i := 0; i < MaxEntries; i++ {
		if _, err := queue.Enqueue(payload(string(rune('a'+i)), 1, "token", 1)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := queue.Enqueue(payload("overflow", 1, "token", 1)); err == nil {
		t.Fatal("queue accepted an entry above its safety limit")
	}
}
