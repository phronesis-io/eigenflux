package discovery

import (
	"context"
	"eigenflux_server/pkg/need"
	"eigenflux_server/rpc/sort/discovery/queryprocessing"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

type queryEmbeddingRecorder struct{ input string }

func (e *queryEmbeddingRecorder) GetEmbedding(_ context.Context, q string) ([]float32, error) {
	e.input = q
	return []float32{1, 0}, nil
}

func TestCompilerFreezesAnalysisWithoutRewritingFilters(t *testing.T) {
	e := &queryEmbeddingRecorder{}
	cc := Compiler{Embedder: e}
	r := Request{Query: "  Ｋ８Ｓ 运维 ", SourceKinds: []Kind{Commission}, Filters: Filters{BudgetMaxFen: num(0), Currency: "CNY", Lang: []string{"zh"}, ExcludeTerms: []string{"广告"}}}
	c, err := cc.Query(context.Background(), 10, 1, 1000, r, "query")
	require.NoError(t, err)
	require.Equal(t, "Ｋ８Ｓ 运维", c.Query)
	require.Equal(t, "k8s 运维", e.input, "embed normalized original, never concatenate expansions")

	require.Equal(t, r.Filters.BudgetMaxFen, c.Filters.BudgetMaxFen)
	require.Equal(t, r.Filters.Lang, c.Filters.Lang)
	require.Equal(t, r.Filters.ExcludeTerms, c.Filters.ExcludeTerms)
	raw, err := json.Marshal(c)
	require.NoError(t, err)
	var back Context
	require.NoError(t, json.Unmarshal(raw, &back))
	require.Equal(t, c.QueryAnalysis, back.QueryAnalysis)
	before := c.SpecHash
	c.QueryAnalysis.Version = "next"
	require.NotEqual(t, before, hashContext(c))

}

func TestScriptAwarePhrasesWithoutVocabulary(t *testing.T) {
	for _, tc := range []struct {
		query  string
		phrase bool
	}{{"人工智能", true}, {"找人工智能的AI服务", false}, {"k8s", false}} {
		c := Context{Query: tc.query, QueryAnalysis: queryprocessing.Process(tc.query, queryprocessing.Options{})}
		body, err := Query(c, Agent, "lexical", 20)
		require.NoError(t, err)
		raw, _ := json.Marshal(body)
		require.Equal(t, tc.phrase, strings.Contains(string(raw), `"type":"phrase"`))
	}
}

func TestLexicalNormalizationRetainsOriginalAnalyzerInput(t *testing.T) {
	c := Context{Query: "ＡＩ", QueryAnalysis: queryprocessing.Process("ＡＩ", queryprocessing.Options{})}
	body, err := Query(c, Agent, "lexical", 20)
	require.NoError(t, err)
	raw, _ := json.Marshal(body)
	require.Contains(t, string(raw), `"query":"ＡＩ"`)
	require.Contains(t, string(raw), `"query":"ai"`)
	require.Contains(t, string(raw), `"tie_breaker":0`)
}

func TestPublicMatchTypesAreAdditiveAndDeduplicated(t *testing.T) {
	c := Candidate{Document: Document{Ref: SourceRef{Type: Agent, ID: 3}, Channels: []string{"dense", "lexical", "lexical"}}}
	r := PublicResponse(Response{Items: []ResultItem{PublicItem(c)}})
	require.Equal(t, []string{"keyword", "semantic"}, r.Items[0].Match["match_types"])
	require.NotContains(t, r.Items[0].Match, "score")
}

func TestAllTextInputsShareQueryProcessing(t *testing.T) {
	for _, tc := range []struct{ goal, context string }{
		{"  Ｋ８Ｓ  ", "运维"},
		{"K8S", "operations"},
		{"寻找人工智能服务", ""},
		{"港股研報", ""},
		{"CV", ""},
	} {
		t.Run(tc.goal, func(t *testing.T) {
			e := &queryEmbeddingRecorder{}
			cc := Compiler{Embedder: e}
			snapshot := capturedFixture(42, Commission)
			editCaptured(t, &snapshot, func(in *need.Input) {
				in.Target = need.Target{Goal: tc.goal, Context: tc.context}
				in.Constraints = need.Constraints{BudgetMaxFen: num(0), Currency: "CNY", Lang: []string{"en"}, ExcludeTerms: []string{"广告"}}
				in.Requirements = []need.Condition{{Text: "Keep data local"}}
			})
			n, err := cc.Need(context.Background(), 10, 1, 1000, snapshot)
			require.NoError(t, err)
			require.NotNil(t, n.QueryAnalysis)
			require.Equal(t, n.QueryAnalysis.Normalized, e.input)
			require.JSONEq(t, string(snapshot.Input), string(n.CapturedNeed.Input))

			for _, origin := range []string{"query", "agent_context"} {
				f := n.Filters
				f.ExcludeAuthors = nil // compileBase adds the same owner exclusion.
				q, err := cc.Query(context.Background(), 10, 2, 1000, Request{Query: tc.goal + "\n" + tc.context, SourceKinds: []Kind{Commission}, Filters: f}, origin)
				require.NoError(t, err)
				require.Equal(t, n.QueryAnalysis, q.QueryAnalysis, origin)

				require.Equal(t, n.Filters, q.Filters, origin)
				require.Equal(t, q.QueryAnalysis.Normalized, e.input)
			}
			snapshot.InputID = 0
			inline, err := cc.Need(context.Background(), 10, 3, 1000, snapshot)
			require.NoError(t, err)
			require.Equal(t, n.QueryAnalysis, inline.QueryAnalysis)
			raw, err := json.Marshal(n)
			require.NoError(t, err)
			var restored Context
			require.NoError(t, json.Unmarshal(raw, &restored))
			require.Equal(t, n.QueryAnalysis, restored.QueryAnalysis)
			for _, channel := range []string{"lexical"} {
				_, err = Query(restored, Commission, channel, 20)
				require.NoError(t, err)
			}
		})
	}
}

func TestBaselineAndIdentityKeepTheirQuerySemantics(t *testing.T) {
	e := &queryEmbeddingRecorder{}
	cc := Compiler{Embedder: e}
	baseline, err := cc.Query(context.Background(), 10, 1, 1000, Request{SourceKinds: []Kind{Broadcast}}, "baseline")
	require.NoError(t, err)
	require.Nil(t, baseline.QueryAnalysis)
	require.Empty(t, e.input)
	exact, err := cc.Query(context.Background(), 10, 2, 1000, Request{Query: "AbCdE", SourceKinds: []Kind{Agent}, agentExact: true}, "query")
	require.NoError(t, err)
	require.True(t, exact.QueryAnalysis.Identity)
	require.Equal(t, "AbCdE", exact.QueryAnalysis.Normalized)

	require.Empty(t, e.input, "exact identity does not need embedding")
	snapshot := capturedFixture(42, Agent)
	editCaptured(t, &snapshot, func(in *need.Input) { in.Target.Goal = "12345" })
	n, err := cc.Need(context.Background(), 10, 3, 1000, snapshot)
	require.NoError(t, err)
	require.False(t, n.QueryAnalysis.Identity, "Need prose must not be reinterpreted as a requested ID")
	require.Equal(t, "12345", e.input)
}
