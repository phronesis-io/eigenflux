package discovery

import (
	"encoding/json"
	"testing"
)

func TestPublicCommissionSummaryPreservesZeroAndMissingEvidence(t *testing.T) {
	for _, kind := range AllKinds {
		doc := baseDoc(kind)
		doc.Preview, doc.PriceFen, doc.Currency, doc.DurationMS = "Review", num(0), "CNY", num(86400000)
		public := PublicResponse(Response{Items: []ResultItem{PublicItem(Candidate{Document: doc, Score: Score{Value: .8}})}})
		encoded, err := json.Marshal(public.Items[0])
		if err != nil {
			t.Fatal(err)
		}
		var item map[string]any
		if err = json.Unmarshal(encoded, &item); err != nil {
			t.Fatal(err)
		}
		if kind == Commission {
			if item["title"] != "Review" || item["price_fen"] != float64(0) || item["currency"] != "CNY" || item["promised_delivery_ms"] != float64(86400000) {
				t.Fatalf("zero price or catalogue evidence lost: %s", encoded)
			}
		} else {
			for _, key := range []string{"title", "price_fen", "currency", "promised_delivery_ms"} {
				if _, present := item[key]; present {
					t.Fatalf("%s leaked Commission %s: %s", kind, key, encoded)
				}
			}
		}
		if _, present := item["match"].(map[string]any)["score"]; present {
			t.Fatal("summary leaked numeric ranking diagnostics")
		}
	}
	var old Response
	if err := json.Unmarshal([]byte(`{"items":[{"source_ref":{"type":"commission","id":"42"},"preview":{"text":"Old cached title"},"match":{}}]}`), &old); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(PublicResponse(old).Items[0])
	if err != nil {
		t.Fatal(err)
	}
	var item map[string]any
	_ = json.Unmarshal(encoded, &item)
	for _, key := range []string{"title", "price_fen", "currency", "promised_delivery_ms"} {
		if _, present := item[key]; present {
			t.Fatalf("old snapshot fabricated %s: %s", key, encoded)
		}
	}
}
