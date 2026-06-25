package consumer

import (
	"encoding/json"
	"testing"

	tradedal "eigenflux_server/rpc/trade/dal"
)

func TestBuildServiceDoc_HasEnrichmentAndEmbeddings(t *testing.T) {
	svc := &tradedal.TradingService{
		ServiceID:          42,
		SellerAgentID:      7,
		Title:              "t",
		CapabilityDesc:     "c",
		CallSpecText:       "s",
		AmountAtomic:       100,
		Asset:              "USDC",
		DeliveryDeadlineMs: 60000,
		Status:             tradedal.ServiceStatusActive,
		UpdatedAt:          1234,
	}
	en := &EnrichOutput{
		CapabilityTags:    []string{"translate:es-zh"},
		UseCases:          "Use when you need Spanish translation.",
		CanonicalInputs:   json.RawMessage(`[]`),
		CanonicalOutputs:  json.RawMessage(`[]`),
		EnrichmentVersion: 1,
	}
	doc := buildServiceDoc(svc, en, []float32{0.1, 0.2}, []float32{0.3, 0.4})

	if doc["service_id"] != int64(42) {
		t.Errorf("service_id: %v", doc["service_id"])
	}
	if doc["status"] != "active" {
		t.Errorf("status: want active, got %v", doc["status"])
	}
	if doc["use_cases"] != "Use when you need Spanish translation." {
		t.Errorf("use_cases: %v", doc["use_cases"])
	}
	if doc["capability_tags"] == nil {
		t.Errorf("capability_tags missing")
	}
	if doc["embedding"] == nil {
		t.Errorf("embedding missing")
	}
	if doc["usage_embedding"] == nil {
		t.Errorf("usage_embedding missing when provided")
	}
}

func TestBuildServiceDoc_OmitsUsageEmbeddingWhenNil(t *testing.T) {
	svc := &tradedal.TradingService{ServiceID: 1, Asset: "USDC", Status: tradedal.ServiceStatusActive}
	en := &EnrichOutput{CapabilityTags: []string{"x"}, UseCases: "Use when needed."}
	doc := buildServiceDoc(svc, en, []float32{0.1}, nil)
	if _, ok := doc["usage_embedding"]; ok {
		t.Errorf("usage_embedding should be omitted when nil")
	}
}

func TestServiceStatusString(t *testing.T) {
	cases := []struct {
		in   int16
		want string
	}{
		{tradedal.ServiceStatusDraft, "draft"},
		{tradedal.ServiceStatusActive, "active"},
		{tradedal.ServiceStatusOffline, "offline"},
		{99, "draft"},
	}
	for _, c := range cases {
		if got := serviceStatusString(c.in); got != c.want {
			t.Errorf("serviceStatusString(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}
