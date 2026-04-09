package es

import (
	"context"
	"eigenflux_server/pkg/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

type mappingResponse map[string]struct {
	Mappings struct {
		Properties map[string]struct {
			Type string `json:"type"`
			Dims int    `json:"dims"`
		} `json:"properties"`
	} `json:"mappings"`
}

func ValidateReadIndexEmbeddingDimensions(ctx context.Context, expected int) (int, error) {
	if expected <= 0 {
		return 0, fmt.Errorf("embedding dimensions are not configured; set EMBEDDING_DIMENSIONS or use a known EMBEDDING_MODEL")
	}

	res, err := Client.Indices.GetMapping(
		Client.Indices.GetMapping.WithContext(ctx),
		Client.Indices.GetMapping.WithIndex(ReadIndexPattern),
	)
	if err != nil {
		return 0, fmt.Errorf("get mapping for %q: %w", ReadIndexPattern, err)
	}
	defer res.Body.Close()

	if res.StatusCode == 404 {
		return 0, fmt.Errorf("no Elasticsearch indices matched %q; recreate the items index after setting the embedding model", ReadIndexPattern)
	}
	if res.IsError() {
		return 0, fmt.Errorf("mapping lookup error: %s", res.String())
	}

	dimsByIndex, err := parseEmbeddingDimensions(res.Body)
	if err != nil {
		return 0, err
	}
	if len(dimsByIndex) == 0 {
		return 0, fmt.Errorf("no embedding field found in indices matching %q", ReadIndexPattern)
	}

	mismatches := make([]string, 0)
	for index, dims := range dimsByIndex {
		if dims != expected {
			mismatches = append(mismatches, fmt.Sprintf("%s=%d", index, dims))
		}
	}
	if len(mismatches) > 0 {
		sort.Strings(mismatches)
		return 0, fmt.Errorf(
			"embedding dimension mismatch: configured model expects %d dims but Elasticsearch indices use %s; recreate or reindex %s after updating EMBEDDING_MODEL/EMBEDDING_DIMENSIONS",
			expected,
			strings.Join(mismatches, ", "),
			ReadIndexPattern,
		)
	}

	return expected, nil
}

func parseEmbeddingDimensions(r io.Reader) (map[string]int, error) {
	var response mappingResponse
	if err := json.NewDecoder(r).Decode(&response); err != nil {
		return nil, fmt.Errorf("decode mapping response: %w", err)
	}

	dimsByIndex := make(map[string]int, len(response))
	for indexName, indexMapping := range response {
		embedding, ok := indexMapping.Mappings.Properties["embedding"]
		if !ok || embedding.Type != "dense_vector" || embedding.Dims <= 0 {
			continue
		}
		dimsByIndex[indexName] = embedding.Dims
	}

	return dimsByIndex, nil
}
