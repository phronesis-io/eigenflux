package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"eigenflux_server/pkg/json"
	sortDal "eigenflux_server/rpc/sort/dal"
	"eigenflux_server/rpc/sort/serviceranker"
)

func TestAugmentItemFeaturesJSON_PreservesExistingFields(t *testing.T) {
	existing := `{"broadcast_type":"news","domains":["ai"],"rank_scores":{"semantic":0.42}}`
	out := augmentItemFeaturesJSON(existing, []string{"normalize:minmax"}, 0.77)

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &got))

	assert.Equal(t, "news", got["broadcast_type"])
	assert.Equal(t, []any{"ai"}, got["domains"])
	assert.Equal(t, "item", got["entry_type"])
	assert.InDelta(t, 0.77, got["normalized_score"].(float64), 1e-9)
	assert.Equal(t, []any{"normalize:minmax"}, got["rerank_reasons"])
}

func TestAugmentItemFeaturesJSON_EmptyInputProducesMinimalRecord(t *testing.T) {
	out := augmentItemFeaturesJSON("", nil, 0.5)
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &got))

	assert.Equal(t, "item", got["entry_type"])
	assert.InDelta(t, 0.5, got["normalized_score"].(float64), 1e-9)
	_, hasReasons := got["rerank_reasons"]
	assert.False(t, hasReasons, "no reasons → key omitted, not empty list")
}

func TestAugmentItemFeaturesJSON_MalformedInputDegrades(t *testing.T) {
	out := augmentItemFeaturesJSON("not-json", []string{"bounds:floor:service"}, 0.1)
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &got))

	assert.Equal(t, "item", got["entry_type"])
	assert.Equal(t, []any{"bounds:floor:service"}, got["rerank_reasons"])
}

func TestBuildServiceFeaturesJSON_FullPayload(t *testing.T) {
	doc := &sortDal.ServiceDoc{
		ServiceID:          1001,
		SellerAgentID:      42,
		Title:              "summarise paper",
		CapabilityDesc:     "Run LLM summarisation",
		Domains:            []string{"research", "nlp"},
		AmountAtomic:       1_000_000,
		Asset:              "USDC",
		DeliveryDeadlineMs: 3600_000,
		UpdatedAt:          1_700_000_000_000,
		SuccessRate:        0.92,
		AvgLatencyMs:       4500,
		OrderCount:         57,
		ReleasedCount:      54,
		RefundedCount:      2,
		ExpiredCount:       1,
		LastActivityAt:     1_700_000_500_000,
	}
	breakdown := serviceranker.ServiceScoreBreakdown{
		Semantic: 0.81, Keyword: 0.62, Success: 0.92,
		Latency: 0.55, Price: 0.30, Deadline: 0.66,
		Total: 0.74,
	}
	reasons := []string{"normalize:minmax", "bounds:floor:service"}

	out := buildServiceFeaturesJSON(doc, breakdown, reasons, 0.88)
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &got))

	assert.Equal(t, "service", got["entry_type"])
	assert.Equal(t, float64(1001), got["service_id"])
	assert.Equal(t, float64(42), got["seller_agent_id"])
	assert.Equal(t, "summarise paper", got["title"])
	assert.Equal(t, "Run LLM summarisation", got["capability_desc"])
	assert.Equal(t, []any{"research", "nlp"}, got["domains"])
	assert.Equal(t, float64(1_000_000), got["amount_atomic"])
	assert.Equal(t, "USDC", got["asset"])
	assert.InDelta(t, 0.88, got["normalized_score"].(float64), 1e-9)
	assert.Equal(t, []any{"normalize:minmax", "bounds:floor:service"}, got["rerank_reasons"])
	assert.Equal(t, []any{serviceRecallSourceName}, got["recall_source_names"])

	rankScores, ok := got["rank_scores"].(map[string]any)
	require.True(t, ok)
	assert.InDelta(t, 0.81, rankScores["semantic"].(float64), 1e-9)
	assert.InDelta(t, 0.74, rankScores["total"].(float64), 1e-9)

	stats, ok := got["stats"].(map[string]any)
	require.True(t, ok)
	assert.InDelta(t, 0.92, stats["success_rate"].(float64), 1e-9)
	assert.Equal(t, float64(54), stats["released_count"])
	assert.Equal(t, float64(1_700_000_500_000), stats["last_activity_at"])
}

func TestBuildServiceFeaturesJSON_OmitsEmptyReasons(t *testing.T) {
	doc := &sortDal.ServiceDoc{ServiceID: 1, Title: "x"}
	out := buildServiceFeaturesJSON(doc, serviceranker.ServiceScoreBreakdown{}, nil, 0.0)
	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	_, hasReasons := got["rerank_reasons"]
	assert.False(t, hasReasons)
}
