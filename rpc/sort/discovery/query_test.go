package discovery

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	searchindex "eigenflux_server/rpc/sort/discovery/index"

	"github.com/stretchr/testify/require"
)

func queryVocabulary() *searchindex.Vocabulary {
	return &searchindex.Vocabulary{Version: "query-fixture", Categories: []searchindex.Node{{ID: "tech", Name: "Technology"}, {ID: "jobs", Name: "Jobs"}}, Intents: []searchindex.Node{
		{ID: "kubernetes", Name: "Kubernetes", Category: "tech", Aliases: []string{"k8s"}},
		{ID: "ai", Name: "Artificial intelligence", Category: "tech", Aliases: []string{"AI", "人工智能", "人工智慧"}},
		{ID: "report", Name: "Research report", Category: "tech", Aliases: []string{"研报", "研報"}},
		{ID: "art", Name: "Art", Category: "tech", Aliases: []string{"illustration"}},
		{ID: "vision", Name: "Computer vision", Category: "tech", Aliases: []string{"CV"}},
		{ID: "resume", Name: "Resume", Category: "jobs", Aliases: []string{"CV"}},
	}}
}

func TestQueryAnalysisGoldenCases(t *testing.T) {
	for _, tc := range []struct {
		query, normalized, script, expanded string
	}{
		{"  Ｋ８Ｓ\t运维  ", "k8s 运维", "mixed", "kubernetes 运维"},
		{"擅长k8s运维", "擅长k8s运维", "mixed", "擅长kubernetes运维"},
		{"K8S operations", "k8s operations", "latin", "kubernetes operations"},
		{"寻找人工智能服务", "寻找人工智能服务", "cjk", "寻找artificial intelligence服务"},
		{"港股研報", "港股研報", "cjk", "港股研报"},
		{"no k8s please", "no k8s please", "latin", "no kubernetes please"},
		{"partial report", "partial report", "latin", ""},
		{"OpenAI services", "openai services", "latin", ""},
		{"abc_k8s", "abc_k8s", "latin", ""},
		{"k8s123", "k8s123", "latin", ""},
		{"eigneflux", "eigneflux", "latin", ""},
		{"軟體架構", "軟體架構", "cjk", ""},         // No implicit simplified/traditional conversion.
		{"running", "running", "latin", ""}, // No unreviewed stemming.
	} {
		t.Run(tc.query, func(t *testing.T) {
			p := analyzeQuery(tc.query, queryVocabulary(), Filters{}, false)
			require.Equal(t, tc.normalized, p.Normalized)
			require.Equal(t, tc.script, p.Script)
			if tc.expanded == "" {
				require.Empty(t, p.Expansions)
			} else {
				queries := []string{}
				for _, e := range p.Expansions {
					queries = append(queries, e.Query)
				}
				require.Contains(t, queries, tc.expanded)
			}
		})
	}
}

func TestQueryAliasesAreScopedBoundedAndDeterministic(t *testing.T) {
	v := queryVocabulary()
	p := analyzeQuery("CV", v, Filters{}, false)
	require.Empty(t, p.Expansions)
	require.Empty(t, p.Intents)
	require.Equal(t, []string{"cv"}, p.Ambiguous)
	p = analyzeQuery("CV", v, Filters{Category: "jobs"}, false)
	require.Equal(t, []QueryExpansion{{From: "cv", To: "resume", Query: "resume", IntentID: "resume"}}, p.Expansions)
	p = analyzeQuery("k8s", v, Filters{Category: "jobs"}, false)
	require.Empty(t, p.Expansions)
	for _, raw := range []string{"9223372036854775807", "AbCdE", "ＡＩ Studio"} {
		p = analyzeQuery(raw, v, Filters{}, true)
		require.Equal(t, raw, p.Normalized)
		require.Equal(t, "identity", p.Script)
		require.Empty(t, p.Expansions)
	}
	for _, alias := range strings.Fields("one two three four five six seven eight nine ten eleven") {
		v.Intents[0].Aliases = append(v.Intents[0].Aliases, alias)
	}
	p = analyzeQuery("k8s", v, Filters{}, false)
	require.Len(t, p.Expansions, maxQueryExpansions)
	for i, j := 0, len(v.Intents)-1; i < j; i, j = i+1, j-1 {
		v.Intents[i], v.Intents[j] = v.Intents[j], v.Intents[i]
	}
	require.Equal(t, p, analyzeQuery("k8s", v, Filters{}, false))
}

func TestQueryLongerPhrasesProtectMeaning(t *testing.T) {
	v := queryVocabulary()
	v.Intents = append(v.Intents, searchindex.Node{ID: "agent", Name: "AI agent", Category: "tech", Aliases: []string{"智能体"}})
	p := analyzeQuery("AI agent", v, Filters{}, false)
	require.Equal(t, []string{"agent"}, p.Intents)
	require.Equal(t, []QueryExpansion{{From: "ai agent", To: "智能体", Query: "智能体", IntentID: "agent"}}, p.Expansions)
	// Latin edges of mixed aliases remain protected.
	v.Intents = append(v.Intents, searchindex.Node{ID: "mixed", Name: "AI设计", Category: "tech", Aliases: []string{"智能设计"}})
	require.Empty(t, analyzeQuery("OpenAI设计", v, Filters{}, false).Expansions)
}

type queryEmbeddingRecorder struct{ input string }

func (e *queryEmbeddingRecorder) GetEmbedding(_ context.Context, q string) ([]float32, error) {
	e.input = q
	return []float32{1, 0}, nil
}

func TestCompilerFreezesAnalysisWithoutRewritingFilters(t *testing.T) {
	e := &queryEmbeddingRecorder{}
	cc := Compiler{Taxonomy: queryVocabulary(), Embedder: e}
	r := Request{Query: "  Ｋ８Ｓ 运维 ", SourceKinds: []Kind{Commission}, Filters: Filters{BudgetMaxFen: num(0), Currency: "CNY", Lang: []string{"zh"}, ExcludeTerms: []string{"广告"}}}
	c, err := cc.Query(context.Background(), 10, 1, 1000, r, "query")
	require.NoError(t, err)
	require.Equal(t, "Ｋ８Ｓ 运维", c.Query)
	require.Equal(t, "k8s 运维", e.input, "embed normalized original, never concatenate expansions")
	require.Equal(t, []string{"kubernetes"}, c.SoftIntents)
	require.Empty(t, c.Filters.Category)
	require.Equal(t, r.Filters.BudgetMaxFen, c.Filters.BudgetMaxFen)
	require.Equal(t, r.Filters.Lang, c.Filters.Lang)
	require.Equal(t, r.Filters.ExcludeTerms, c.Filters.ExcludeTerms)
	row, err := encode(c)
	require.NoError(t, err)
	var back Context
	require.NoError(t, json.Unmarshal([]byte(row.Compiled), &back))
	require.Equal(t, c.QueryAnalysis, back.QueryAnalysis)
	before := c.SpecHash
	c.QueryAnalysis.Version = "next"
	require.NotEqual(t, before, hashContext(c))
	// Dictionary ambiguity must not re-enter through exact taxonomy matching.
	c, err = cc.Query(context.Background(), 10, 2, 1000, Request{Query: "cv", SourceKinds: []Kind{Agent}}, "query")
	require.NoError(t, err)
	require.Empty(t, c.SoftIntents)
}

func TestSynonymQueryPreservesFiltersAndRequiresExpandedPhrase(t *testing.T) {
	c := Context{Query: "擅长k8s运维", Filters: Filters{Category: "tech", Lang: []string{"zh"}, ExcludeAuthors: []string{"42"}, BudgetMaxFen: num(0), Currency: "CNY"}}
	c.QueryAnalysis = analyzeQuery(c.Query, queryVocabulary(), c.Filters, false)
	body, err := Query(c, Commission, "synonym", 20)
	require.NoError(t, err)
	raw, _ := json.Marshal(body)
	for _, field := range []string{"retrieval_slots.category", "retrieval_slots.lang", "seller_agent_id", "price_fen", "currency", "must_not", "kubernetes", `"type":"phrase"`, `"boost":0.5`, `"tie_breaker":0`} {
		require.Contains(t, string(raw), field)
	}
	_, err = Query(Context{}, Agent, "synonym", 20)
	require.Error(t, err)
	for _, query := range []string{"人工智能", "找人工智能的AI服务"} {
		c = Context{Query: query, QueryAnalysis: analyzeQuery(query, queryVocabulary(), Filters{}, false)}
		body, err = Query(c, Agent, "lexical", 20)
		require.NoError(t, err)
		raw, _ = json.Marshal(body)
		require.Contains(t, string(raw), `"type":"phrase"`)
	}
	body, _ = Query(Context{Query: "k8s", QueryAnalysis: analyzeQuery("k8s", queryVocabulary(), Filters{}, false)}, Agent, "lexical", 20)
	raw, _ = json.Marshal(body)
	require.NotContains(t, string(raw), `"type":"phrase"`, "Latin-only input uses token matching")
}

func TestLexicalNormalizationRetainsOriginalAnalyzerInput(t *testing.T) {
	c := Context{Query: "ＡＩ", QueryAnalysis: analyzeQuery("ＡＩ", queryVocabulary(), Filters{}, false)}
	body, err := Query(c, Agent, "lexical", 20)
	require.NoError(t, err)
	raw, _ := json.Marshal(body)
	require.Contains(t, string(raw), `"query":"ＡＩ"`)
	require.Contains(t, string(raw), `"query":"ai"`)
	require.Contains(t, string(raw), `"tie_breaker":0`)
}

func TestPublicMatchTypesAreAdditiveAndDeduplicated(t *testing.T) {
	c := Candidate{Document: Document{Ref: SourceRef{Type: Agent, ID: 3}, Channels: []string{"dense", "synonym", "lexical", "synonym"}}}
	r := PublicResponse(Response{Items: []ResultItem{PublicItem(c)}})
	require.Equal(t, []string{"keyword", "semantic", "synonym"}, r.Items[0].Match["match_types"])
	require.NotContains(t, r.Items[0].Match, "score")
}
