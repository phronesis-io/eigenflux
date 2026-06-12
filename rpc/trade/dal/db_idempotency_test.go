package dal

import (
	"errors"
	"testing"
	"time"

	"eigenflux_server/pkg/db"

	"gorm.io/gorm"
)

func TestFindOrderByIdempotencyKey(t *testing.T) {
	setupDAL(t)
	buyerID := int64(time.Now().UnixNano() % 1_000_000)
	key := "test-idemp-key-1"
	orderID := time.Now().UnixNano()
	order := &TradeOrder{
		OrderID:                  orderID,
		ServiceID:                1,
		BuyerAgentID:             buyerID,
		SellerAgentID:            2,
		Status:                   OrderStatusCreated,
		FrozenTitle:              "x",
		FrozenAmountAtomic:       1000,
		FrozenAsset:              "USDC",
		FrozenDeliveryDeadlineMs: 60_000,
		IdempotencyKey:           key,
	}
	if err := CreateOrder(db.DB, order); err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	defer db.DB.Exec("DELETE FROM trade_orders WHERE order_id = ?", orderID)

	got, err := FindOrderByIdempotencyKey(db.DB, buyerID, key)
	if err != nil {
		t.Fatalf("FindOrderByIdempotencyKey: %v", err)
	}
	if got.OrderID != orderID {
		t.Fatalf("expected order_id=%d, got %d", orderID, got.OrderID)
	}

	_, err = FindOrderByIdempotencyKey(db.DB, buyerID, "no-such-key")
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("expected ErrRecordNotFound, got %v", err)
	}
}
