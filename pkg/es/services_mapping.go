package es

// BuildServicesMapping defines the static and slow-changing fields a service
// document carries in ES. Rank-time signals that change with every completed
// order (success_rate, avg_latency_ms, order_count) are intentionally NOT
// indexed here: they would force a re-index on every order event, while the
// ranker can fetch them from PostgreSQL in a single batched query after recall.
// Keeping ES write traffic limited to service publish/update keeps the index
// stable and avoids version churn on the embedding field.
func BuildServicesMapping(embeddingDims int) map[string]interface{} {
	return map[string]interface{}{
		"properties": map[string]interface{}{
			"service_id":           map[string]interface{}{"type": "long"},
			"seller_agent_id":      map[string]interface{}{"type": "long"},
			"status":               map[string]interface{}{"type": "keyword"},
			"title":                map[string]interface{}{"type": "text", "analyzer": "standard"},
			"capability_desc":      map[string]interface{}{"type": "text", "analyzer": "standard"},
			"call_spec_text":       map[string]interface{}{"type": "text", "analyzer": "standard"},
			"keywords":             map[string]interface{}{"type": "keyword", "normalizer": "lowercase_normalizer"},
			"domains":              map[string]interface{}{"type": "keyword", "normalizer": "lowercase_normalizer"},
			"capability_tags":      map[string]interface{}{"type": "keyword", "normalizer": "lowercase_normalizer"},
			"use_cases":            map[string]interface{}{"type": "text", "analyzer": "standard"},
			"embedding":            map[string]interface{}{"type": "dense_vector", "dims": embeddingDims, "index": true, "similarity": "cosine"},
			"usage_embedding":      map[string]interface{}{"type": "dense_vector", "dims": embeddingDims, "index": true, "similarity": "cosine"},
			"amount_atomic":        map[string]interface{}{"type": "long"},
			"asset":                map[string]interface{}{"type": "keyword"},
			"delivery_deadline_ms": map[string]interface{}{"type": "long"},
			"updated_at":           map[string]interface{}{"type": "date", "format": "strict_date_optional_time||epoch_millis"},
		},
	}
}
