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
}

func checkBuyerGate(db *gorm.DB, buyerAgentID int64, maxActive int) (*GateResult, error) {
	activeCount, err := dal.CountActiveOrders(db, buyerAgentID)
	if err != nil {
		return nil, err
	}

	hasPending, err := dal.HasPendingRelease(db, buyerAgentID)
	if err != nil {
		return nil, err
	}

	result := &GateResult{
		ActiveCount:       int32(activeCount),
		MaxActive:         int32(maxActive),
		HasPendingRelease: hasPending,
	}
	result.CanCreate = activeCount < int64(maxActive) && !hasPending
	return result, nil
}
