package main

import (
	"context"
	"eigenflux_server/pkg/config"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestNeedRecallRejectsOutOfRangeBeforeStartingDependencies(t *testing.T) {
	for _, bps := range []int{-1, 10001} {
		_, _, err := initDiscovery(context.Background(), &config.Config{EnableNeedSearch: true, DiscoveryNeedRecallBPS: bps}, nil, nil, nil, nil)
		require.EqualError(t, err, "DISCOVERY_NEED_RECALL_BPS must be in [0,10000]")
	}
}
