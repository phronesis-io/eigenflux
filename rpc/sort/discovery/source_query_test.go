package discovery

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAllChannelsCarryExplicitFilters(t *testing.T) {
	budget := int64(0)
	deadline := int64(200)
	c := Context{CreatedAt: 100, Query: "designer", Vector: []float32{1, 0}, Filters: Filters{ExcludeAuthors: []string{"42"}, Lang: []string{"en"}, ProviderRegion: []string{"US"}, BudgetMaxFen: &budget, Currency: "CNY", DeadlineMS: &deadline}}
	for _, channel := range []string{"lexical", "dense"} {
		q, err := Query(c, Commission, channel, 20)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(q)
		for _, term := range []string{"seller_agent_id", "retrieval_slots.provider_region", "retrieval_slots.lang", "price_fen", "promised_delivery_ms", "currency", "active", "must_not"} {
			if !strings.Contains(string(b), term) {
				t.Fatalf("%s omits %s", channel, term)
			}
		}
	}
	q, _ := Query(Context{Query: "x", Filters: Filters{ExcludeAuthors: []string{"42"}}}, Agent, "lexical", 20)
	b, _ := json.Marshal(q)
	if strings.Contains(string(b), "private") {
		t.Fatal("private field queried")
	}
}

func TestBroadcastUsesExactTopLevelLanguageInEveryChannel(t *testing.T) {
	c := Context{Query: "design", Vector: []float32{1, 0}, Filters: Filters{Lang: []string{"zh-CN"}}}
	for _, channel := range []string{"lexical", "dense"} {
		q, err := Query(c, Broadcast, channel, 20)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(q)
		if !strings.Contains(string(b), `"terms":{"lang.keyword":["zh-CN"]}`) || strings.Contains(string(b), `"terms":{"lang":`) || strings.Contains(string(b), `"retrieval_slots.lang"`) {
			t.Fatalf("%s must use exact top-level language: %s", channel, b)
		}
	}
}

func TestBroadcastRetrievalFiltersExpiryAtRequestClock(t *testing.T) {
	c := Context{Query: "EigenFlux", Vector: []float32{1, 0}, retrievalAt: 1791017820000}
	for _, channel := range []string{"lexical", "lexical_recent"} {
		q, err := Query(c, Broadcast, channel, 20)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(q)
		if !strings.Contains(string(b), `"expire_time":{"gt":"2026-10-03T08:57:00Z"}`) || !strings.Contains(string(b), `"exists":{"field":"expire_time"}`) {
			t.Fatalf("expiry filter missing: %s", b)
		}
	}

	dense, err := Query(c, Broadcast, "dense", 20)
	if err != nil {
		t.Fatal(err)
	}
	denseJSON, _ := json.Marshal(dense)
	if strings.Contains(string(denseJSON), "expire_time") || strings.Contains(string(denseJSON), `"timeout"`) {
		t.Fatalf("dense retrieval policy changed: %s", denseJSON)
	}
	q, err := Query(c, Broadcast, "lexical_recent", 20)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(q)
	for _, want := range []string{`"gte":"2026-09-26T08:57:00Z"`, `"lte":"2026-10-03T08:57:00Z"`, `"track_scores":true`, `"timeout":"1s"`, `"created_at":"desc"`, `"multi_match"`} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("recent query omits %s: %s", want, b)
		}
	}
	for _, kind := range []Kind{Agent, Commission} {
		if _, err := Query(c, kind, "lexical_recent", 20); err == nil {
			t.Fatal("non-broadcast recent channel accepted")
		}
	}
}
