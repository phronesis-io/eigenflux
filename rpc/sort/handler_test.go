package main

import (
	"context"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"

	sortapi "eigenflux_server/kitex_gen/eigenflux/sort"
	"eigenflux_server/rpc/sort/discovery"
)

func TestDiscoveryHandlerGuardsAndWireErrors(t *testing.T) {
	disabled := &SortServiceESImpl{}
	for _, tc := range []struct {
		req    *sortapi.DiscoveryReq
		code   int32
		reason string
	}{
		{nil, 401, "unauthorized"},
		{&sortapi.DiscoveryReq{AgentId: 1, Operation: "search", Payload: `{"query":"design"}`}, 503, "discovery_disabled"},
	} {
		out, err := disabled.Discovery(context.Background(), tc.req)
		if err != nil || out.BaseResp.Code != tc.code || out.BaseResp.Msg != tc.reason {
			t.Fatal(out, err)
		}
	}
	enabled := &SortServiceESImpl{discovery: &discovery.Service{}}
	out, err := enabled.Discovery(context.Background(), &sortapi.DiscoveryReq{AgentId: 1, Operation: "unknown", Payload: "{}"})
	if err != nil || out.BaseResp.Code != 400 {
		t.Fatal(out, err)
	}
	var problem discovery.Error
	if err := json.Unmarshal([]byte(out.Payload), &problem); err != nil || problem.Code != 400 {
		t.Fatal(problem, err)
	}
}

func TestDiscoveryHandlerRejectsRemovedOperation(t *testing.T) {
	handler := &SortServiceESImpl{discovery: &discovery.Service{Engine: &discovery.Engine{Compiler: &discovery.Compiler{}}}}
	out, err := handler.Discovery(context.Background(), &sortapi.DiscoveryReq{AgentId: 1, Operation: "taxonomy", Payload: `{}`})
	require.NoError(t, err)
	require.EqualValues(t, 400, out.BaseResp.Code)
}
