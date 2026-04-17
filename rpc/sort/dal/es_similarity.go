package dal

import (
	"bytes"
	"context"
	"eigenflux_server/pkg/json"
	"fmt"

	"eigenflux_server/pkg/es"
)

// SearchSimilarItems searches for similar items using embedding
func SearchSimilarItems(ctx context.Context, embedding []float32, threshold float32, limit int) ([]Item, error) {
	if limit <= 0 {
		limit = 5
	}
	if expectedDims := es.EmbeddingDimensions(); expectedDims > 0 && len(embedding) != expectedDims {
		return nil, fmt.Errorf("embedding dimension mismatch: query vector=%d, index=%d", len(embedding), expectedDims)
	}

	// Build kNN query
	query := map[string]interface{}{
		"knn": map[string]interface{}{
			"field":          "embedding",
			"query_vector":   embedding,
			"k":              limit,
			"num_candidates": limit * 10,
			"filter": map[string]interface{}{
				"exists": map[string]interface{}{
					"field": "group_id",
				},
			},
		},
		"_source": []string{"id", "group_id", "content", "summary", "author_agent_id", "created_at", "type"},
	}

	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(query); err != nil {
		return nil, fmt.Errorf("encode query: %w", err)
	}

	res, err := es.Client.Search(
		es.Client.Search.WithContext(ctx),
		es.Client.Search.WithIndex(es.ReadIndexPattern),
		es.Client.Search.WithBody(&buf),
	)
	if err != nil {
		return nil, fmt.Errorf("execute search: %w", err)
	}
	defer res.Body.Close()

	if res.IsError() {
		return nil, fmt.Errorf("ES search error: %s", res.String())
	}

	var result map[string]interface{}
	if err := json.NewDecoder(res.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	hits := result["hits"].(map[string]interface{})["hits"].([]interface{})
	items := make([]Item, 0, len(hits))

	for _, hit := range hits {
		hitMap := hit.(map[string]interface{})

		// ES kNN returns transformed _score: (1 + cosine_similarity) / 2
		// Need to convert back to original cosine similarity: cosine = 2 * score - 1
		score, ok := hitMap["_score"].(float64)
		if !ok {
			continue
		}

		cosineSim := float32(2*score - 1)

		source := hitMap["_source"].(map[string]interface{})
		itemID, _ := source["id"].(float64)

		fmt.Printf("[ES Similarity] Item %d: ES score=%.4f, cosine=%.4f, threshold=%.2f\n",
			int64(itemID), score, cosineSim, threshold)

		if cosineSim < threshold {
			fmt.Printf("[ES Similarity] Item %d filtered out (%.4f < %.2f)\n", int64(itemID), cosineSim, threshold)
			continue // Skip results below threshold
		}

		var item Item
		sourceJSON, _ := json.Marshal(source)
		if err := json.Unmarshal(sourceJSON, &item); err != nil {
			continue
		}
		item.Score = float64(cosineSim)
		items = append(items, item)
	}

	return items, nil
}
