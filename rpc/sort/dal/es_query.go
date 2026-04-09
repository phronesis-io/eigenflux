package dal

import (
	"bytes"
	"context"
	"eigenflux_server/pkg/json"
	"fmt"
	"strings"
	"time"

	"eigenflux_server/pkg/es"
	"eigenflux_server/pkg/logger"
)

const (
	defaultFreshnessOffset = "12h"
	defaultFreshnessScale  = "7d"
	defaultFreshnessDecay  = 0.8
)

// buildSearchQuery builds the Elasticsearch query based on search parameters
func buildSearchQuery(req *SearchItemsRequest) map[string]interface{} {
	logger.Default().Debug("building ES query", "domains", req.Domains, "keywords", req.Keywords, "geo", req.Geo)

	// Resolve freshness parameters with defaults
	offset := req.FreshnessOffset
	if offset == "" {
		offset = defaultFreshnessOffset
	}
	scale := req.FreshnessScale
	if scale == "" {
		scale = defaultFreshnessScale
	}
	decay := req.FreshnessDecay
	if decay == 0 {
		decay = defaultFreshnessDecay
	}

	mustClauses := []interface{}{}

	// 1. Expire time filter: expire_time is null or greater than current time
	mustClauses = append(mustClauses, map[string]interface{}{
		"bool": map[string]interface{}{
			"should": []interface{}{
				map[string]interface{}{
					"bool": map[string]interface{}{
						"must_not": map[string]interface{}{
							"exists": map[string]interface{}{
								"field": "expire_time",
							},
						},
					},
				},
				map[string]interface{}{
					"range": map[string]interface{}{
						"expire_time": map[string]interface{}{
							"gte": time.Now().Format(time.RFC3339),
						},
					},
				},
			},
			"minimum_should_match": 1,
		},
	})

	// 2. domains, keywords, geo filtering (using query context for relevance scoring)
	shouldClauses := []interface{}{}

	if len(req.Domains) > 0 {
		for _, domain := range req.Domains {
			lowercaseDomain := strings.ToLower(domain)
			shouldClauses = append(shouldClauses, map[string]interface{}{
				"bool": map[string]interface{}{
					"should": []interface{}{
						// Exact match (highest weight)
						map[string]interface{}{
							"term": map[string]interface{}{
								"domains": map[string]interface{}{
									"value": lowercaseDomain,
									"boost": 3.0,
								},
							},
						},
						// Fuzzy match (second highest weight)
						map[string]interface{}{
							"match": map[string]interface{}{
								"domains.text": map[string]interface{}{
									"query": lowercaseDomain,
									"boost": 2.0,
								},
							},
						},
					},
					"minimum_should_match": 1,
				},
			})
		}
	}

	if len(req.Keywords) > 0 {
		for _, keyword := range req.Keywords {
			lowercaseKeyword := strings.ToLower(keyword)
			shouldClauses = append(shouldClauses, map[string]interface{}{
				"bool": map[string]interface{}{
					"should": []interface{}{
						map[string]interface{}{
							"term": map[string]interface{}{
								"keywords": map[string]interface{}{
									"value": lowercaseKeyword,
									"boost": 3.0,
								},
							},
						},
						map[string]interface{}{
							"match": map[string]interface{}{
								"keywords.text": map[string]interface{}{
									"query": lowercaseKeyword,
									"boost": 2.0,
								},
							},
						},
					},
					"minimum_should_match": 1,
				},
			})
		}
	}

	if req.Geo != "" {
		shouldClauses = append(shouldClauses, map[string]interface{}{
			"match": map[string]interface{}{
				"geo": map[string]interface{}{
					"query": req.Geo,
					"boost": 1.5,
				},
			},
		})
	}

	// If there are should conditions, add them to bool query (OR relationship)
	if len(shouldClauses) > 0 {
		logger.Default().Debug("adding should clauses with relevance scoring", "count", len(shouldClauses))
		mustClauses = append(mustClauses, map[string]interface{}{
			"bool": map[string]interface{}{
				"should":               shouldClauses,
				"minimum_should_match": 1,
			},
		})
	} else {
		logger.Default().Debug("no should clauses (no domains/keywords/geo)")
	}

	// Build final query
	query := map[string]interface{}{
		"query": map[string]interface{}{
			"function_score": map[string]interface{}{
				"query": map[string]interface{}{
					"bool": map[string]interface{}{
						"must": mustClauses,
					},
				},
				"functions": []interface{}{
					map[string]interface{}{
						"gauss": map[string]interface{}{
							"updated_at": map[string]interface{}{
								"origin": "now",
								"offset": offset,
								"scale":  scale,
								"decay":  decay,
							},
						},
					},
				},
				"score_mode": "multiply",
				"boost_mode": "multiply",
			},
		},
		"sort": []interface{}{
			map[string]interface{}{
				"_score": map[string]interface{}{
					"order": "desc",
				},
			},
			map[string]interface{}{
				"updated_at": map[string]interface{}{
					"order": "desc",
				},
			},
		},
		"size": req.Limit,
	}

	// Avoid pretty-printing the full query unless debug logging is enabled.
	if logger.DebugEnabled() {
		queryJSON, err := json.MarshalIndent(query, "", "  ")
		if err != nil {
			logger.Default().Warn("failed to marshal final ES query", "err", err)
		} else {
			logger.Default().Debug("final ES query", "query", string(queryJSON))
		}
	}

	return query
}

// DeleteItem deletes an item from Elasticsearch using delete_by_query
// This works across multiple indices after ILM rollover
func DeleteItem(ctx context.Context, itemID int64) error {
	// Build delete query matching the item ID
	query := map[string]interface{}{
		"query": map[string]interface{}{
			"term": map[string]interface{}{
				"id": itemID,
			},
		},
	}

	body, err := json.Marshal(query)
	if err != nil {
		return fmt.Errorf("failed to marshal delete query: %w", err)
	}

	// Use DeleteByQuery API which works with index patterns
	res, err := es.Client.DeleteByQuery(
		[]string{es.ReadIndexPattern},
		bytes.NewReader(body),
		es.Client.DeleteByQuery.WithContext(ctx),
	)
	if err != nil {
		return fmt.Errorf("failed to delete item: %w", err)
	}
	defer res.Body.Close()

	if res.IsError() {
		return fmt.Errorf("ES delete error: %s", res.String())
	}

	return nil
}

// BulkIndexItems indexes multiple items in a single request
func BulkIndexItems(ctx context.Context, items []Item) error {
	if len(items) == 0 {
		return nil
	}

	var buf bytes.Buffer
	for _, item := range items {
		// Action line
		action := map[string]interface{}{
			"index": map[string]interface{}{
				"_index": es.IndexName,
				"_id":    fmt.Sprintf("%d", item.ID),
			},
		}
		actionJSON, _ := json.Marshal(action)
		buf.Write(actionJSON)
		buf.WriteByte('\n')

		// Document line
		docJSON, _ := json.Marshal(item)
		buf.Write(docJSON)
		buf.WriteByte('\n')
	}

	res, err := es.Client.Bulk(
		bytes.NewReader(buf.Bytes()),
		es.Client.Bulk.WithContext(ctx),
	)
	if err != nil {
		return fmt.Errorf("failed to bulk index: %w", err)
	}
	defer res.Body.Close()

	if res.IsError() {
		return fmt.Errorf("ES bulk error: %s", res.String())
	}

	return nil
}
