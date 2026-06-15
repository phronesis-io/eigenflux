package dal

import (
	"testing"
	"time"

	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
)

func setupDAL(t *testing.T) {
	t.Helper()
	cfg := config.Load()
	db.Init(cfg.PgDSN)
}

func TestInsertOutbox_RoundTrip(t *testing.T) {
	setupDAL(t)
	row := &TradeOutbox{
		OutboxID:    time.Now().UnixNano(),
		StreamName:  "stream:trade:order-event",
		PayloadJSON: `{"order_id":"1","service_id":"2","event_type":"released","outbox_id":"3"}`,
		CreatedAt:   time.Now().UnixMilli(),
	}
	if err := InsertOutbox(db.DB, row); err != nil {
		t.Fatalf("InsertOutbox: %v", err)
	}
	defer db.DB.Exec("DELETE FROM trade_outbox WHERE outbox_id = ?", row.OutboxID)

	pending, err := ListPendingOutbox(db.DB, 10)
	if err != nil {
		t.Fatalf("ListPendingOutbox: %v", err)
	}
	found := false
	for _, p := range pending {
		if p.OutboxID == row.OutboxID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("inserted outbox row not in pending list")
	}

	now := time.Now().UnixMilli()
	if err := MarkOutboxPublished(db.DB, row.OutboxID, now); err != nil {
		t.Fatalf("MarkOutboxPublished: %v", err)
	}

	pending2, _ := ListPendingOutbox(db.DB, 10)
	for _, p := range pending2 {
		if p.OutboxID == row.OutboxID {
			t.Fatalf("published row still in pending list")
		}
	}
}

func TestDeleteOldPublishedOutbox(t *testing.T) {
	setupDAL(t)
	id := time.Now().UnixNano() + 1
	publishedAt := int64(1)
	old := &TradeOutbox{
		OutboxID:    id,
		StreamName:  "stream:trade:order-event",
		PayloadJSON: `{}`,
		Status:      1,
		CreatedAt:   1,
		PublishedAt: &publishedAt,
	}
	if err := db.DB.Create(old).Error; err != nil {
		t.Fatalf("create old: %v", err)
	}
	defer db.DB.Exec("DELETE FROM trade_outbox WHERE outbox_id = ?", id)

	if _, err := DeleteOldPublishedOutbox(db.DB, time.Now().UnixMilli()); err != nil {
		t.Fatalf("DeleteOldPublishedOutbox: %v", err)
	}

	var remaining int64
	if err := db.DB.Model(&TradeOutbox{}).Where("outbox_id = ?", id).Count(&remaining).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("expected outbox_id=%d to be deleted, %d rows remain", id, remaining)
	}
}
