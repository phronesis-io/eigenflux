package queryprocessing

import (
	searchindex "eigenflux_server/rpc/sort/discovery/index"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
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
			p := Process(tc.query, queryVocabulary(), Options{})
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
	p := Process("CV", v, Options{})
	require.Empty(t, p.Expansions)
	require.Empty(t, p.Intents)
	require.Equal(t, []string{"cv"}, p.Ambiguous)
	p = Process("CV", v, Options{Category: "jobs"})
	require.Equal(t, []Expansion{{From: "cv", To: "resume", Query: "resume", IntentID: "resume"}}, p.Expansions)
	p = Process("k8s", v, Options{Category: "jobs"})
	require.Empty(t, p.Expansions)
	for _, raw := range []string{"9223372036854775807", "AbCdE", "ＡＩ Studio"} {
		p = Process(raw, v, Options{Identity: true})
		require.Equal(t, raw, p.Normalized)
		require.Equal(t, "identity", p.Script)
		require.Empty(t, p.Expansions)
	}
	for _, alias := range strings.Fields("one two three four five six seven eight nine ten eleven") {
		v.Intents[0].Aliases = append(v.Intents[0].Aliases, alias)
	}
	p = Process("k8s", v, Options{})
	require.Len(t, p.Expansions, MaxExpansions)
	for i, j := 0, len(v.Intents)-1; i < j; i, j = i+1, j-1 {
		v.Intents[i], v.Intents[j] = v.Intents[j], v.Intents[i]
	}
	require.Equal(t, p, Process("k8s", v, Options{}))
}

func TestQueryLongerPhrasesProtectMeaning(t *testing.T) {
	v := queryVocabulary()
	v.Intents = append(v.Intents, searchindex.Node{ID: "agent", Name: "AI agent", Category: "tech", Aliases: []string{"智能体"}})
	p := Process("AI agent", v, Options{})
	require.Equal(t, []string{"agent"}, p.Intents)
	require.Equal(t, []Expansion{{From: "ai agent", To: "智能体", Query: "智能体", IntentID: "agent"}}, p.Expansions)
	// Latin edges of mixed aliases remain protected.
	v.Intents = append(v.Intents, searchindex.Node{ID: "mixed", Name: "AI设计", Category: "tech", Aliases: []string{"智能设计"}})
	require.Empty(t, Process("OpenAI设计", v, Options{}).Expansions)
}

func TestProcessingWithoutAliases(t *testing.T) {
	p := Process("  Ｋ８Ｓ\t运维 ", nil, Options{})
	require.Equal(t, "k8s 运维", p.Normalized)
	require.Equal(t, "mixed", p.Script)
	require.Empty(t, p.Expansions)
}
