package discoverysource

import (
	"eigenflux_server/pkg/discovery"
	"encoding/json"
	"strings"
	"testing"
)

func TestAllChannelsCarryExplicitFilters(t *testing.T) {
	budget := int64(0)
	deadline := int64(200)
	c := discovery.Context{CreatedAt: 100, Query: "designer", Vector: []float32{1, 0}, Filters: discovery.Filters{Category: "design", TaxonomyVersion: "v1", ExcludeAuthors: []string{"42"}, Lang: []string{"en"}, ProviderRegion: []string{"US"}, BudgetMaxFen: &budget, Currency: "CNY", DeadlineMS: &deadline}, SoftIntents: []string{"web"}}
	for _, channel := range []string{"lexical", "dense", "structured"} {
		q, err := Query(c, discovery.Commission, channel, 20)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(q)
		for _, term := range []string{"seller_agent_id", "retrieval_slots.category", "retrieval_slots.provider_region", "retrieval_slots.lang", "price_fen", "promised_delivery_ms", "currency", "active", "must_not"} {
			if !strings.Contains(string(b), term) {
				t.Fatalf("%s omits %s", channel, term)
			}
		}
	}
	q, _ := Query(discovery.Context{Query: "x", Filters: discovery.Filters{ExcludeAuthors: []string{"42"}}}, discovery.Agent, "lexical", 20)
	b, _ := json.Marshal(q)
	if strings.Contains(string(b), "private") {
		t.Fatal("private field queried")
	}
}
