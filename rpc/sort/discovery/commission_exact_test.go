package discovery

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExactCommissionRequestContract(t *testing.T) {
	r, err := Decode[Request]([]byte(`{"commission_id":"9223372036854775807"}`))
	require.NoError(t, err)
	r, err = NormalizeRequest(r, Search, 100)
	require.NoError(t, err)
	require.Equal(t, int64(9223372036854775807), r.CommissionID)
	require.Equal(t, []Kind{Commission}, r.SourceKinds)
	for _, raw := range []string{`{"commission_id":"0"}`, `{"commission_id":"-1"}`, `{"commission_id":"9223372036854775808"}`, `{"commission_id":42}`, `{"commission_id":null}`} {
		_, err := Decode[Request]([]byte(raw))
		require.Error(t, err, raw)
	}
	for _, r := range []Request{
		{CommissionID: 42, Query: "design"}, {CommissionID: 42, NeedID: 7},
		{CommissionID: 42, Need: &NeedInput{}}, {CommissionID: 42, NeedIDs: []string{"7"}},
		{CommissionID: 42, SourceKinds: []Kind{Agent}},
		{CommissionID: 42, SourceKinds: []Kind{Commission, Agent}},
	} {
		_, err := NormalizeRequest(r, Search, 100)
		require.Error(t, err, r)
	}
	_, err = NormalizeRequest(Request{CommissionID: 42}, Recommendation, 100)
	require.Error(t, err)
}

func TestExactCommissionQueryKeepsFilters(t *testing.T) {
	minPrice, duration := int64(50), int64(1000)
	c := Context{Origin: "query", Query: "9223372036854775807", Filters: Filters{Currency: "CNY", MinPriceFen: &minPrice, MaxDurationMS: &duration}}
	body, err := Query(c, Commission, "exact", 100)
	require.NoError(t, err)
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	for _, fragment := range []string{`"size":1`, `"commission_id":9223372036854775807`, `"active":true`, `"currency":"CNY"`, `"price_fen":{"gte":50}`, `"promised_delivery_ms":{"lte":1000}`} {
		require.Contains(t, string(raw), fragment)
	}
	require.NotContains(t, string(raw), "knn")
	require.NotContains(t, string(raw), "multi_match")
	// An ordinary numeric text query retains its full-text search contract.
	body, err = Query(c, Commission, "lexical", 20)
	require.NoError(t, err)
	raw, err = json.Marshal(body)
	require.NoError(t, err)
	require.Contains(t, string(raw), "multi_match")
	require.NotContains(t, string(raw), `"term":{"commission_id"`)
}

func TestExactCommissionKeepsHardFiltersAndNeverBroadens(t *testing.T) {
	e, s, _ := engineFixture()
	e.Compiler.Embedder = unexpectedEmbedding{t}
	e.Rules[Commission][Search] = Rule{Version: "strict", BM25Scale: 1, MinRelevance: 1, Threshold: 1, HalfLifeMS: 1000}
	price, duration := int64(100), int64(1000)
	d := Document{Ref: SourceRef{Commission, 42}, AuthorID: 20, Version: "1", Active: true, Visible: true, ExactMatch: "commission_id", PriceFen: &price, Currency: "CNY", DurationMS: &duration}
	s.docs = []Document{{Ref: SourceRef{Commission, 99}, AuthorID: 21, Active: true, Visible: true, Lexical: 100}}
	for _, reason := range []string{"valid", "missing", "inactive", "blocked", "self", "price", "duration", "currency"} {
		t.Run(reason, func(t *testing.T) {
			candidate := d
			r := Request{CommissionID: 42}
			s.exact = []Document{candidate}
			switch reason {
			case "missing":
				s.exact = nil
			case "inactive":
				s.exact[0].Active = false
			case "blocked":
				s.exact[0].Blocked = true
			case "self":
				s.exact[0].AuthorID = 1
			case "price":
				max := int64(99)
				r.Filters = Filters{Currency: "CNY", MaxPriceFen: &max}
			case "duration":
				max := int64(999)
				r.Filters.MaxDurationMS = &max
			case "currency":
				r.Filters.Currency = "USD"
			}
			x, err := e.Execute(context.Background(), 1, r, Search, 100)
			require.NoError(t, err)
			require.Empty(t, x.PartialReasons)
			if reason != "valid" {
				require.Empty(t, x.Candidates)
				return
			}
			require.Len(t, x.Candidates, 1)
			require.Equal(t, int64(42), x.Candidates[0].Document.Ref.ID)
			require.Equal(t, "exact_match", x.Candidates[0].Score.Kind)
			require.Equal(t, "commission_identity_v1", x.Candidates[0].Score.Version)
		})
	}
}
