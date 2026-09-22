package main

import (
	"context"
	"encoding/json"
	"testing"

	sortapi "eigenflux_server/kitex_gen/eigenflux/sort"
	"eigenflux_server/rpc/sort/discovery"
	searchindex "eigenflux_server/rpc/sort/discovery/index"
)

type handlerEmbedder struct{}

func (handlerEmbedder) GetEmbedding(context.Context, string) ([]float32, error) {
	return []float32{1, 0}, nil
}

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

func TestDiscoveryHandlerForwardsTaxonomyContract(t *testing.T) {
	handler := &SortServiceESImpl{discovery: &discovery.Service{Engine: &discovery.Engine{Compiler: &discovery.Compiler{
		Taxonomy: &searchindex.Vocabulary{Version: "fixture", Intents: []searchindex.Node{{ID: "design", Name: "design", Vector: []float32{1, 0}}}}, Embedder: handlerEmbedder{},
	}}}}
	out, err := handler.Discovery(context.Background(), &sortapi.DiscoveryReq{AgentId: 1, Operation: "taxonomy", Payload: `{"query":"design","limit":1}`})
	if err != nil || out.BaseResp.Code != 0 {
		t.Fatal(out, err)
	}
	var result struct {
		Version string              `json:"taxonomy_version"`
		Matches []searchindex.Match `json:"matches"`
	}
	if err := json.Unmarshal([]byte(out.Payload), &result); err != nil || result.Version != "fixture" || len(result.Matches) != 1 || result.Matches[0].ID != "design" {
		t.Fatal(result, err)
	}
}
