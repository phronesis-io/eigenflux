package discovery

import (
	"context"
	"eigenflux_server/pkg/need"
	searchindex "eigenflux_server/rpc/sort/discovery/index"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHistoricalCardLanguagesPassEquivalentConstraints(t *testing.T) {
	for _, tc := range []struct {
		requested, actual string
		accepted          bool
	}{
		{"en", "English", true}, {"English", "en", true}, {"en", "eNgLiSh", true},
		{"zh", "中文", true}, {"中文", "zh", true}, {"zh", "chinese", true},
		{"Chinese", "中文", true}, {"en-US", "EN-us", true}, {"zh-CN", "zh-cn", true},
		{"en", "en-US", false}, {"en-US", "English", false}, {"zh", "zh-CN", false},
		{"中文", "zh-TW", false}, {"zh-CN", "zh-TW", false},
		{"en", "English · Chinese", false}, {"English · Chinese", "en", false},
		{"en", "Englİsh", false}, {"en", " English ", false},
		{"es", "Spanish", false}, {"Spanish", "spanish", false},
		{"English · Chinese", "English · Chinese", true},
	} {
		t.Run(tc.requested+"/"+tc.actual, func(t *testing.T) {
			c, doc := baseContext(), baseDoc(Agent)
			c.Filters.Lang = []string{tc.requested}
			doc.Slots.Lang = []string{tc.actual}
			if tc.accepted {
				require.Empty(t, Check(c, doc, Recommendation, 100))
			} else {
				require.Equal(t, "language", Check(c, doc, Recommendation, 100))
			}
		})
	}
}

func TestCardLanguageInheritanceAndNeedIntersection(t *testing.T) {
	for _, tc := range []struct{ card, code string }{{"English", "en"}, {"中文", "zh"}, {"chinese", "zh"}} {
		t.Run(tc.card, func(t *testing.T) {
			e, source, store := engineFixture()
			source.owner = OwnerContext{Languages: []string{tc.card}, Clauses: []string{"design"}}
			source.docs = []Document{{Ref: SourceRef{Broadcast, 9}, AuthorID: 2, Version: "1", Active: true, Visible: true, Lexical: 10, Slots: searchindex.Slots{Lang: []string{tc.code}}}}
			for _, mode := range []Mode{Search, Recommendation} {
				r := Request{SourceKinds: []Kind{Broadcast}, Defaults: Defaults{Language: "card"}}
				if mode == Search {
					r.Query = "design"
				}
				x, err := e.Execute(context.Background(), 1, r, mode, 100)
				require.NoError(t, err)
				require.Len(t, x.Candidates, 1)
				require.Equal(t, []string{tc.card}, x.Candidates[0].Context.Filters.Lang, "keep the explicit Card evidence in samples")
			}
			snapshot := capturedFixture(7, Broadcast)
			editCaptured(t, &snapshot, func(in *need.Input) { in.Constraints.Lang = []string{tc.code} })
			store.active = []need.Snapshot{snapshot}
			x, err := e.Execute(context.Background(), 1, Request{SourceKinds: []Kind{Broadcast}, Defaults: Defaults{Language: "card"}}, Recommendation, 100)
			require.NoError(t, err, "an equivalent inherited language must not conflict with a Need")
			require.Len(t, x.Candidates, 1)
			require.Equal(t, []string{tc.code}, x.Candidates[0].Context.Filters.Lang)
			require.Equal(t, string(snapshot.Input), string(x.Candidates[0].Context.CapturedNeed.Input))
			require.Equal(t, []string{tc.card}, source.owner.Languages, "do not rewrite Card source data")
		})
	}
}

func TestLanguageIntersectionPreservesSourceAndRegionBoundaries(t *testing.T) {
	a := Filters{Lang: []string{"en", "zh-CN"}, ProviderRegion: []string{"US"}}
	b := Filters{Lang: []string{"English", "zh-cn"}, ProviderRegion: []string{"US"}}
	out, err := IntersectFilters(a, b)
	require.NoError(t, err)
	require.Equal(t, a.Lang, out.Lang)
	require.Equal(t, []string{"English", "zh-cn"}, b.Lang)
	for _, tc := range [][2]string{{"en", "en-US"}, {"zh", "zh-CN"}, {"zh-CN", "zh-TW"}, {"en", "English · Chinese"}} {
		_, err = IntersectFilters(Filters{Lang: []string{tc[0]}}, Filters{Lang: []string{tc[1]}})
		require.Error(t, err)
	}
	_, err = IntersectFilters(Filters{ProviderRegion: []string{"US"}}, Filters{ProviderRegion: []string{"us"}})
	require.Error(t, err, "language compatibility must not change region matching")
}

func TestAgentNeedLanguageKeepsOriginalCandidateAndSourceEvidence(t *testing.T) {
	e, source, store := engineFixture()
	snapshot := capturedFixture(7, Agent)
	editCaptured(t, &snapshot, func(in *need.Input) { in.Constraints.Lang = []string{"en"} })
	store.active = []need.Snapshot{snapshot}
	source.docs = []Document{{Ref: SourceRef{Agent, 9}, AuthorID: 2, Version: "1", Active: true, Visible: true, Lexical: 10, Slots: searchindex.Slots{Lang: []string{"English"}}}}
	x, err := e.Execute(context.Background(), 1, Request{SourceKinds: []Kind{Agent}}, Recommendation, 100)
	require.NoError(t, err)
	require.Len(t, x.Candidates, 1)
	candidate := x.Candidates[0]
	require.Equal(t, []string{"English"}, candidate.Document.Slots.Lang)
	require.Equal(t, []string{"en"}, candidate.Context.Filters.Lang)
	require.Equal(t, string(snapshot.Input), string(candidate.Context.CapturedNeed.Input))
	require.Equal(t, needCompilerVersion, candidate.Context.CompilerVersion)
	raw, err := json.Marshal(candidate)
	require.NoError(t, err)
	var frozen Candidate
	require.NoError(t, json.Unmarshal(raw, &frozen))
	require.Equal(t, candidate.Document.Slots.Lang, frozen.Document.Slots.Lang)
	require.Equal(t, candidate.Context.Filters.Lang, frozen.Context.Filters.Lang)
}

func TestCachedLanguageEvidenceIsReadWithoutMutation(t *testing.T) {
	cc := Compiler{Cache: contextCacheFixture(t)}
	ctx, now := context.Background(), time.Now().UnixMilli()
	r := Request{Query: "design", SourceKinds: []Kind{Broadcast}, Filters: Filters{Lang: []string{"English"}}}
	first, err := cc.Query(ctx, 1, 101, now, r, "agent_context")
	require.NoError(t, err)
	first.Filters.Lang[0] = "poison"
	warm, err := cc.Query(ctx, 1, 102, now+1, r, "agent_context")
	require.NoError(t, err)
	require.Equal(t, []string{"English"}, warm.Filters.Lang)
	require.Equal(t, []string{"English"}, r.Filters.Lang)
	require.Empty(t, Check(warm, baseDoc(Broadcast), Recommendation, now+1))
	// Earlier wire snapshots keep their raw evidence; compatibility does not
	// require a schema migration or mutating old cached/public source values.
	warm.CompilerVersion = "context_rules_v6"
	raw, err := json.Marshal(warm)
	require.NoError(t, err)
	var historical Context
	require.NoError(t, json.Unmarshal(raw, &historical))
	require.Empty(t, Check(historical, baseDoc(Broadcast), Recommendation, now+1))
	query, err := Query(historical, Broadcast, "lexical", 20)
	require.NoError(t, err)
	raw, err = json.Marshal(query)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"value":"en"`)
	require.Contains(t, string(raw), `"value":"english"`)
	require.Equal(t, []string{"English"}, historical.Filters.Lang)
}
