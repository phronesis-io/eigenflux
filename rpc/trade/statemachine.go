package main

import (
	"fmt"
	"slices"

	"eigenflux_server/rpc/trade/dal"
)

// validTransitions maps each non-terminal state to the states it may move to.
// The escrow_locked and seller_cancelled states from the previous chief escrow
// model are no longer reachable. The refunded target was retired with the
// removal of RefundOrder; see docs/dev/trading.md.
var validTransitions = map[int16][]int16{
	dal.OrderStatusCreated:   {dal.OrderStatusDelivered, dal.OrderStatusExpired},
	dal.OrderStatusDelivered: {dal.OrderStatusReleased},
}

func validateTransition(from, to int16) error {
	allowed, ok := validTransitions[from]
	if !ok {
		return fmt.Errorf("no transitions from status %d", from)
	}
	if slices.Contains(allowed, to) {
		return nil
	}
	return fmt.Errorf("invalid transition: %d -> %d", from, to)
}

// isTerminalStatus covers released, refunded (historical only), and expired.
// expired joined the terminal set when RefundOrder was removed.
func isTerminalStatus(status int16) bool {
	return status == dal.OrderStatusReleased ||
		status == dal.OrderStatusRefunded ||
		status == dal.OrderStatusExpired
}

func isActiveStatus(status int16) bool {
	return status == dal.OrderStatusCreated || status == dal.OrderStatusDelivered
}
