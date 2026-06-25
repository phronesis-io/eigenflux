package main

import (
	"testing"

	"eigenflux_server/rpc/trade/dal"
)

// TestNoRefundTransitions asserts that the refund target is no longer reachable
// from any state via validateTransition. RefundOrder has been removed; the only
// terminal entries are released (from delivered) and expired (from created).
func TestNoRefundTransitions(t *testing.T) {
	cases := []struct {
		name string
		from int16
	}{
		{"delivered_to_refunded", dal.OrderStatusDelivered},
		{"expired_to_refunded", dal.OrderStatusExpired},
		{"created_to_refunded", dal.OrderStatusCreated},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := validateTransition(c.from, dal.OrderStatusRefunded); err == nil {
				t.Fatalf("expected error transitioning %d -> refunded, got nil", c.from)
			}
		})
	}

	if err := validateTransition(dal.OrderStatusCreated, dal.OrderStatusDelivered); err != nil {
		t.Fatalf("created -> delivered must remain valid: %v", err)
	}
	if err := validateTransition(dal.OrderStatusCreated, dal.OrderStatusExpired); err != nil {
		t.Fatalf("created -> expired must remain valid: %v", err)
	}
	if err := validateTransition(dal.OrderStatusDelivered, dal.OrderStatusReleased); err != nil {
		t.Fatalf("delivered -> released must remain valid: %v", err)
	}
}

// TestExpiredIsTerminal asserts that expired is now a true terminal state — it
// participates in isTerminalStatus alongside released and the historical
// refunded.
func TestExpiredIsTerminal(t *testing.T) {
	if !isTerminalStatus(dal.OrderStatusExpired) {
		t.Fatal("OrderStatusExpired must be terminal after refund removal")
	}
	if !isTerminalStatus(dal.OrderStatusReleased) {
		t.Fatal("OrderStatusReleased must remain terminal")
	}
	if !isTerminalStatus(dal.OrderStatusRefunded) {
		t.Fatal("OrderStatusRefunded must remain terminal for historical rows")
	}
	if isTerminalStatus(dal.OrderStatusCreated) {
		t.Fatal("OrderStatusCreated must not be terminal")
	}
	if isTerminalStatus(dal.OrderStatusDelivered) {
		t.Fatal("OrderStatusDelivered must not be terminal")
	}
}
