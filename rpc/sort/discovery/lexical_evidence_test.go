package discovery

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHanPairsPreservePositionsAndBoundLongInputs(t *testing.T) {
	require.Equal(t, []string{"游戏", "游标"}, hanPairs("游戏 Agent 游标 游戏"))
	require.Empty(t, hanPairs("游 Agent 标"))
	require.Empty(t, hanPairs("agent systems"))
	var text strings.Builder
	for r := rune(0x4e00); r < 0x4e00+1000; r++ {
		text.WriteRune(r)
	}
	pairs := hanPairs(text.String())
	require.Len(t, pairs, 64)
	require.Equal(t, string([]rune{0x4e00, 0x4e01}), pairs[0])
	require.Equal(t, string([]rune{0x4e00 + 998, 0x4e00 + 999}), pairs[63])
}

func TestLexicalEvidenceIsBroadcastOnlyAndPreservesHardFilters(t *testing.T) {
	c := Context{Query: "游戏 Agent", retrievalAt: 1000, Filters: Filters{ExcludeAuthors: []string{"10"}, Lang: []string{"zh"}}}
	baseline, err := Query(c, Broadcast, "lexical", 20)
	require.NoError(t, err)
	c.LexicalPolicy = LexicalEvidenceV1
	repaired, err := Query(c, Broadcast, "lexical", 20)
	require.NoError(t, err)
	b, _ := json.Marshal(baseline)
	r, _ := json.Marshal(repaired)
	require.NotContains(t, string(b), "domains.text")
	require.NotContains(t, string(r), "domains.text", "long/mixed text must not broaden through generic topic labels")
	require.Contains(t, string(r), `"query":"游戏"`)
	require.Contains(t, string(r), `"type":"phrase"`)
	before := baseline["query"].(map[string]any)["bool"].(map[string]any)
	after := repaired["query"].(map[string]any)["bool"].(map[string]any)
	require.Equal(t, before["must_not"], after["must_not"])
	oldFilters := before["filter"].([]any)
	newFilters := after["filter"].([]any)
	require.Equal(t, oldFilters, newFilters[:len(oldFilters)])
	for _, kind := range []Kind{Agent, Commission} {
		changed, err := Query(c, kind, "lexical", 20)
		require.NoError(t, err)
		c.LexicalPolicy = ""
		legacy, err := Query(c, kind, "lexical", 20)
		require.NoError(t, err)
		require.Equal(t, legacy, changed)
		c.LexicalPolicy = LexicalEvidenceV1
	}
}

func TestLexicalEvidenceArmDoesNotMutateFrozenContext(t *testing.T) {
	e, _, _ := engineFixture()
	contexts := []Context{{ID: 42, OwnerID: 1, Query: "游戏", State: "active", Origin: "query", Kinds: []Kind{Broadcast}}}
	e.LexicalEvidenceBPS = 10000
	run, err := e.ReplayContexts(context.Background(), 1, contexts, []Kind{Broadcast}, 20, 1000)
	require.NoError(t, err)
	require.Equal(t, LexicalEvidenceV1, run.Contexts[0].LexicalPolicy)
	require.Empty(t, contexts[0].LexicalPolicy)
	e.LexicalEvidenceBPS = 0
	run, err = e.ReplayContexts(context.Background(), 1, contexts, []Kind{Broadcast}, 20, 1000)
	require.NoError(t, err)
	require.Empty(t, run.Contexts[0].LexicalPolicy)
}

func TestShortLatinTopicIsBounded(t *testing.T) {
	for _, s := range []string{"ai-agents", "multi-agent-systems", "workflow automation"} {
		require.True(t, shortLatinTopic(s))
	}
	for _, s := range []string{"", "AI 游戏", "find practical agent tools for workflows", "foo/bar"} {
		require.False(t, shortLatinTopic(s))
	}
	c := Context{Query: "ai-agents", LexicalPolicy: LexicalEvidenceV1}
	q, err := Query(c, Broadcast, "lexical", 20)
	require.NoError(t, err)
	raw, _ := json.Marshal(q)
	require.Contains(t, string(raw), `"match_phrase":{"domains.text":"ai-agents"}`)
}
