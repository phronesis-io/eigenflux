package main

import (
	"context"
	"eigenflux_server/pkg/config"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestNeedBudgetRejectsOutOfRangeBeforeStartingDependencies(t *testing.T) {
	for _, bps := range []int{-1, 10001} {
		_, _, err := initDiscovery(context.Background(), &config.Config{EnableNeedSearch: true, DiscoveryNeedBudgetBPS: bps}, nil, nil, nil, nil)
		require.EqualError(t, err, "DISCOVERY_NEED_BUDGET_BPS must be in [0,10000]")
	}
}
