package main

import (
	"context"
	"eigenflux_server/pkg/config"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestNeedRelativeRejectsOutOfRangeBeforeStartingDependencies(t *testing.T) {
	for _, bps := range []int{-1, 10001} {
		_, _, err := initDiscovery(context.Background(), &config.Config{EnableNeedSearch: true, DiscoveryNeedRelativeBPS: bps}, nil, nil, nil, nil)
		require.EqualError(t, err, "DISCOVERY_NEED_RELATIVE_BPS must be in [0,10000]")
	}
}

func TestGBDTDefaultsOffAndRejectsOutOfRange(t *testing.T) {
	t.Setenv("DISCOVERY_GBDT_BPS", "")
	t.Setenv("DISCOVERY_GBDT_MODEL_PATH", "")
	cfg := config.Load()
	require.Zero(t, cfg.DiscoveryGBDTBPS)
	require.Equal(t, "/data/models/eigenflux/discovery-gbdt/current/model.json", cfg.DiscoveryGBDTModelPath)
	for _, bps := range []int{-1, 10001} {
		_, _, err := initDiscovery(context.Background(), &config.Config{EnableNeedSearch: true, DiscoveryGBDTBPS: bps}, nil, nil, nil, nil)
		require.EqualError(t, err, "DISCOVERY_GBDT_BPS must be in [0,10000]")
	}
}
