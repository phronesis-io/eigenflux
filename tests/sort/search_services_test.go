package sort_test

import (
	"context"
	"fmt"
	"testing"

	"eigenflux_server/kitex_gen/eigenflux/sort"
	"eigenflux_server/kitex_gen/eigenflux/sort/sortservice"
	"eigenflux_server/kitex_gen/eigenflux/trade"
	"eigenflux_server/kitex_gen/eigenflux/trade/tradeservice"
	"eigenflux_server/pkg/config"

	"github.com/cloudwego/kitex/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newSearchServicesClients builds Sort + Trade RPC clients connected directly by port.
func newSearchServicesClients(t *testing.T) (sortservice.Client, tradeservice.Client) {
	t.Helper()
	cfg := config.Load()
	sortCli, err := sortservice.NewClient("SortService",
		client.WithHostPorts(fmt.Sprintf("127.0.0.1:%d", cfg.SortRPCPort)),
	)
	require.NoError(t, err)
	tradeCli, err := tradeservice.NewClient("TradeService",
		client.WithHostPorts(fmt.Sprintf("127.0.0.1:%d", cfg.TradeRPCPort)),
	)
	require.NoError(t, err)
	return sortCli, tradeCli
}

// publishSearchTestService creates one service via trade and returns its ID.
func publishSearchTestService(t *testing.T, cli tradeservice.Client, sellerID int64, title, capability string) int64 {
	t.Helper()
	resp, err := cli.PublishService(context.Background(), &trade.PublishServiceReq{
		SellerAgentId:      sellerID,
		Title:              title,
		CapabilityDesc:     capability,
		CallSpecText:       "Call me with {input: string}",
		PriceText:          "10 USDC",
		AmountAtomic:       10_000_000,
		Asset:              "USDC",
		DeliveryDeadlineMs: 3_600_000,
	})
	require.NoError(t, err)
	require.Equal(t, int32(0), resp.BaseResp.Code, "publish failed: %s", resp.BaseResp.Msg)
	require.NotZero(t, resp.ServiceId)
	return resp.ServiceId
}

// TestSearchServices_AgentProvidedSubIntents is a smoke test: register
// four services covering distinct capabilities, then submit a multi-intent
// task search with agent-supplied sub_intents. The test verifies that:
//   - the RPC returns successfully (no error, BaseResp.Code == 0),
//   - debug.sub_intents_source == "agent",
//   - debug.effective_sub_intents echoes the four intents we sent.
//
// The number of results depends on whether the LLM enrichment pipeline has
// completed (services need usage_embedding and capability_tags to be most
// recall-friendly). The test logs the result count but does NOT assert
// coverage — that would be flaky pre-enrichment. A full coverage test
// requires waiting on `enrichment_version > 0` for each service and is left
// to a follow-up integration suite that can also gate on a real LLM endpoint.
func TestSearchServices_AgentProvidedSubIntents(t *testing.T) {
	sortCli, tradeCli := newSearchServicesClients(t)
	sellerID := int64(99002001)

	publishSearchTestService(t, tradeCli, sellerID,
		"ES->ZH Translator", "Spanish to Chinese translation service.")
	publishSearchTestService(t, tradeCli, sellerID,
		"Presentation Maker", "Generate presentation slides from an outline.")
	publishSearchTestService(t, tradeCli, sellerID,
		"Spanish Literature Expert", "Domain analysis of Spanish-language source material.")
	publishSearchTestService(t, tradeCli, sellerID,
		"Slide Image Generator", "Generate images for slide decks.")

	limit := int32(10)
	imp1 := 1.0
	imp2 := 0.9
	imp3 := 0.7
	imp4 := 0.4
	req := &sort.SearchServicesReq{
		RawQuery: "Translate Spanish source material and generate PPT for presentation.",
		SubIntents: []*sort.SubIntent{
			{Name: "translate", QueryText: "Spanish to Chinese translation", Importance: &imp1},
			{Name: "ppt_gen", QueryText: "Generate presentation slides from outline", Importance: &imp2},
			{Name: "domain", QueryText: "Expert analysis of Spanish-language source", Importance: &imp3},
			{Name: "image", QueryText: "Generate presentation images", Importance: &imp4},
		},
		Limit: &limit,
	}
	resp, err := sortCli.SearchServices(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, resp.BaseResp)
	require.Equal(t, int32(0), resp.BaseResp.Code, "RPC error: %s", resp.BaseResp.Msg)

	require.NotNil(t, resp.Debug, "debug envelope required")
	assert.Equal(t, "agent", resp.Debug.SubIntentsSource)
	assert.Len(t, resp.Debug.EffectiveSubIntents, 4)

	t.Logf("SearchServices: source=%s effective_intents=%d results=%d",
		resp.Debug.SubIntentsSource, len(resp.Debug.EffectiveSubIntents), len(resp.Results))
	for _, r := range resp.Results {
		t.Logf("  service_id=%d title=%q score=%.4f matched=%v winning=%q",
			r.ServiceId, r.Title, r.Score, r.MatchedIntents, r.WinningIntent)
	}
}

// TestSearchServices_EmptyRawQueryRejected verifies the request validator.
func TestSearchServices_EmptyRawQueryRejected(t *testing.T) {
	sortCli, _ := newSearchServicesClients(t)
	resp, err := sortCli.SearchServices(context.Background(), &sort.SearchServicesReq{
		RawQuery: "",
	})
	require.NoError(t, err)
	assert.Equal(t, int32(400), resp.BaseResp.Code)
}
