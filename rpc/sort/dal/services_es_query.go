package dal

import (
	"eigenflux_server/pkg/json"
	"eigenflux_server/pkg/logger"
)

// SearchServicesRequest represents the search request parameters
type SearchServicesRequest struct {
	Query   string   // free text query
	Domains []string // domain filter
	Limit   int

	// Optional cap filters used by SearchServices (T12 fan-out).
	// Zero disables the corresponding filter.
	MaxPriceAtomic int64
	DeadlineMsMax  int64
}

// SearchServicesResponse represents the search response
type SearchServicesResponse struct {
	Services []ServiceDoc
	Total    int64
}

// buildServiceSearchQuery builds the Elasticsearch query for service search
func buildServiceSearchQuery(req *SearchServicesRequest) map[string]interface{} {
	logger.Default().Debug("building ES services query", "query", req.Query, "domains", req.Domains)

	filterClauses := []interface{}{
		map[string]interface{}{
			"term": map[string]interface{}{
				"status": "active",
			},
		},
	}

	if len(req.Domains) > 0 {
		filterClauses = append(filterClauses, map[string]interface{}{
			"terms": map[string]interface{}{
				"domains": req.Domains,
			},
		})
	}

	if req.MaxPriceAtomic > 0 {
		filterClauses = append(filterClauses, map[string]interface{}{
			"range": map[string]interface{}{
				"amount_atomic": map[string]interface{}{"lte": req.MaxPriceAtomic},
			},
		})
	}

	if req.DeadlineMsMax > 0 {
		filterClauses = append(filterClauses, map[string]interface{}{
			"range": map[string]interface{}{
				"delivery_deadline_ms": map[string]interface{}{"lte": req.DeadlineMsMax},
			},
		})
	}

	boolQuery := map[string]interface{}{
		"filter": filterClauses,
	}

	if req.Query != "" {
		boolQuery["must"] = []interface{}{
			map[string]interface{}{
				"multi_match": map[string]interface{}{
					"query":  req.Query,
					"fields": []string{"title^2", "capability_desc", "call_spec_text", "use_cases", "keywords"},
					"type":   "best_fields",
				},
			},
		}
	}

	query := map[string]interface{}{
		"query": map[string]interface{}{
			"bool": boolQuery,
		},
		"sort": []interface{}{
			map[string]interface{}{
				"_score": map[string]interface{}{
					"order": "desc",
				},
			},
		},
		"size": req.Limit,
	}

	if logger.DebugEnabled() {
		queryJSON, err := json.MarshalIndent(query, "", "  ")
		if err != nil {
			logger.Default().Warn("failed to marshal final ES services query", "err", err)
		} else {
			logger.Default().Debug("final ES services query", "query", string(queryJSON))
		}
	}

	return query
}
