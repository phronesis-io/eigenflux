package main

import (
	"testing"

	"eigenflux_server/api/consolev2"
	"eigenflux_server/pkg/config"
	"github.com/cloudwego/hertz/pkg/app/server"
)

func TestConsoleSnapshotFileRouteIsRegistered(t *testing.T) {
	h := server.New()
	registerConsoleV2BusinessBFF(h, &consolev2.Service{}, &config.Config{})
	for _, route := range h.Routes() {
		if route.Method == "GET" && route.Path == "/api/v2/console/bff/trade/orders/:order_id/snapshots/:snapshot_id/file" {
			return
		}
	}
	t.Fatal("snapshot file BFF route missing")
}
