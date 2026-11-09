package discoverye2e

import (
	"eigenflux_server/rpc/sort/discovery"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestDiscoveryLexicalEvidencePolicyE2E(t *testing.T) {
	s := startStack(t, map[string]string{"DISCOVERY_LEXICAL_EVIDENCE_BPS": "10000"})
	s.changeInputs(t, "UPDATE agent_context_revisions SET compiled_context=?::jsonb WHERE agent_id=?", `{"intent_actions":[{"watch_for":"landing page design","trigger_when":""}]}`, s.owner)
	result := s.recommend(t, discovery.Request{SourceKinds: []discovery.Kind{discovery.Broadcast}, Limit: 5}, "lexical-evidence")
	require.Len(t, result.Items, 1)
	require.Equal(t, s.item, result.Items[0].Ref.ID)
	s.waitSamples(t, result.ImpressionID, 1)
	var raw string
	require.NoError(t, s.db.Table("replay_logs").Select("item_features").Where("impression_id=? AND item_id=?", result.ImpressionID, s.item).Scan(&raw).Error)
	sample := decode[struct {
		Search discovery.Candidate `json:"search"`
	}](t, []byte(raw))
	require.Equal(t, discovery.LexicalEvidenceV1, sample.Search.Context.LexicalPolicy)
	// A retry returns the frozen page, including its original impression.
	retry := s.recommend(t, discovery.Request{SourceKinds: []discovery.Kind{discovery.Broadcast}, Limit: 5}, "lexical-evidence")
	require.Equal(t, result.ImpressionID, retry.ImpressionID)
	require.Equal(t, result.Items, retry.Items)
}
