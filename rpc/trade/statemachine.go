package main

import (
	"fmt"

	"eigenflux_server/rpc/trade/dal"
)

// validTransitions maps each non-terminal state to the states it may move to.
// The escrow_locked and seller_cancelled states from the previous chief escrow
// model are no longer reachable; see docs/dev/trading.md.
var validTransitions = map[int16][]int16{
	dal.OrderStatusCreated:   {dal.OrderStatusDelivered, dal.OrderStatusExpired},
	dal.OrderStatusDelivered: {dal.OrderStatusReleased, dal.OrderStatusRefunded, dal.OrderStatusExpired},
	dal.OrderStatusExpired:   {dal.OrderStatusRefunded},
}

func validateTransition(from, to int16) error {
	allowed, ok := validTransitions[from]
	if !ok {
		return fmt.Errorf("no transitions from status %d", from)
	}
	for _, a := range allowed {
		if a == to {
			return nil
		}
	}
	return fmt.Errorf("invalid transition: %d -> %d", from, to)
}

func isTerminalStatus(status int16) bool {
	return status == dal.OrderStatusReleased || status == dal.OrderStatusRefunded
}

func isActiveStatus(status int16) bool {
	return status == dal.OrderStatusCreated || status == dal.OrderStatusDelivered
}
