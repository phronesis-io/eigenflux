package main

import (
	"eigenflux_server/rpc/trade/dal"
	"gorm.io/gorm"
)

type GateResult struct {
	CanCreate         bool
	ActiveCount       int32
	MaxActive         int32
	HasPendingRelease bool
	UnpaidOrderCount  int32
}

func checkBuyerGate(db *gorm.DB, buyerAgentID int64, maxActive int) (*GateResult, error) {
	activeCount, err := dal.CountActiveOrders(db, buyerAgentID)
	if err != nil {
		return nil, err
	}

	unpaidCount, err := dal.CountUnpaidOrders(db, buyerAgentID)
	if err != nil {
		return nil, err
	}

	result := &GateResult{
		ActiveCount:       int32(activeCount),
		MaxActive:         int32(maxActive),
		UnpaidOrderCount:  int32(unpaidCount),
		HasPendingRelease: unpaidCount > 0,
	}
	result.CanCreate = activeCount < int64(maxActive) && unpaidCount == 0
	return result, nil
}
