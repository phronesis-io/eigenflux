package main

import (
	"context"
	"strings"
	"testing"

	"eigenflux_server/kitex_gen/eigenflux/profile"
)

func TestNameSearchRejectsInvalidInputBeforeDAL(t *testing.T) {
	service := &ProfileServiceImpl{}
	for _, req := range []*profile.MatchAgentsByNameReq{nil, {Query: ""}, {Query: strings.Repeat("中", 101)}, {Query: "\x00"}, {Query: "\xff"}} {
		got, err := service.MatchAgentsByName(context.Background(), req)
		if err != nil || got.BaseResp.Code != 400 || len(got.AgentIds) != 0 {
			t.Fatalf("%+v %v", got, err)
		}
	}
}
