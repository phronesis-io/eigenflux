package es

// BuildIndexMapping returns the Elasticsearch mapping for the item index.
func BuildIndexMapping(embeddingDims int) map[string]interface{} {
	return map[string]interface{}{
		"properties": map[string]interface{}{
			"id": map[string]interface{}{
				"type": "long",
			},
			"content": map[string]interface{}{
				"type": "text",
			},
			"extra": map[string]interface{}{
				"type":    "object",
				"enabled": true,
			},
			"raw_url": map[string]interface{}{
				"type": "keyword",
			},
			"summary": map[string]interface{}{
				"type": "text",
			},
			"type": map[string]interface{}{
				"type": "keyword",
			},
			"domains": map[string]interface{}{
				"type":       "keyword",
				"normalizer": "lowercase_normalizer",
				"fields": map[string]interface{}{
					"text": map[string]interface{}{
						"type":     "text",
						"analyzer": "standard",
					},
				},
			},
			"expire_time": map[string]interface{}{
				"type":   "date",
				"format": "strict_date_optional_time||yyyy-MM-dd HH:mm:ss||yyyy-MM-dd||epoch_millis",
			},
			"geo": map[string]interface{}{
				"type": "text",
				"fields": map[string]interface{}{
					"keyword": map[string]interface{}{
						"type": "keyword",
					},
				},
			},
			"geo_country": map[string]interface{}{
				"type": "keyword",
			},
			"source_type": map[string]interface{}{
				"type": "keyword",
			},
			"expected_response": map[string]interface{}{
				"type": "text",
			},
			"keywords": map[string]interface{}{
				"type":       "keyword",
				"normalizer": "lowercase_normalizer",
				"fields": map[string]interface{}{
					"text": map[string]interface{}{
						"type":     "text",
						"analyzer": "standard",
					},
				},
			},
			"group_id": map[string]interface{}{
				"type": "long",
			},
			"embedding": map[string]interface{}{
				"type":       "dense_vector",
				"dims":       embeddingDims,
				"index":      true,
				"similarity": "cosine",
			},
			"created_at": map[string]interface{}{
				"type":   "date",
				"format": "strict_date_optional_time||epoch_millis",
			},
			"updated_at": map[string]interface{}{
				"type":   "date",
				"format": "strict_date_optional_time||epoch_millis",
			},
		},
	}
}
