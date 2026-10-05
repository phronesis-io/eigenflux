package legacy

import (
	"context"
	"testing"

	"eigenflux_server/pkg/agentutility"
	"eigenflux_server/rpc/sort/discovery"
	"eigenflux_server/rpc/sort/discoverylr"
	"eigenflux_server/rpc/sort/rerank"

	"github.com/stretchr/testify/require"
)

func TestDiscoveryOperationalPromotionIsRecommendationOnly(t *testing.T) {
	service := &Service{itemRerankPolicies: &rerankPolicySet{policies: []rerank.Policy{&rerank.BoostPolicy{Rules: []rerank.BoostRule{{Field: "agent_utility", Values: []string{agentutility.WorkflowRecipe}, Weight: 1.25}}}}}}
	for _, learned := range []bool{false, true} {
		for _, mode := range []discovery.Mode{discovery.Recommendation, discovery.Search} {
			ordinary := discovery.Candidate{Context: discovery.Context{ID: 1}, Document: discovery.Document{Ref: discovery.SourceRef{Type: discovery.Broadcast, ID: 1}}, Score: discovery.Score{Value: .9, Eligible: true}}
			useful := ordinary
			useful.Document.Ref.ID = 2
			useful.Score = discovery.Score{Value: .8, Eligible: true}
			useful.Document.AgentUtility = agentutility.Classify("uvx mcp-server-git --repository .")
			if learned {
				ordinary.LR = &discoverylr.Result{Probability: .9}
				useful.LR = &discoverylr.Result{Probability: .8}
			}
			out, err := service.DiscoveryPolicies(context.Background(), []discovery.Candidate{ordinary, useful}, mode, 1)
			require.NoError(t, err)
			require.Len(t, out, 2)
			if mode == discovery.Recommendation {
				require.EqualValues(t, 2, out[0].Document.Ref.ID)
				require.InDelta(t, 1, out[0].FinalScore, 1e-9)
				require.Contains(t, out[0].Reasons, "boost:agent_utility=workflow_recipe")
			} else {
				require.EqualValues(t, 1, out[0].Document.Ref.ID)
				require.Empty(t, out[1].Reasons)
			}
			require.Equal(t, .8, useful.Score.Value, "promotion must not change the relevance gate or model inputs")
		}
	}
}
