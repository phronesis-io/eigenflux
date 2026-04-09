package dal

import (
	"bytes"
	"context"
	"eigenflux_server/pkg/json"
	"fmt"
	"io"
	"strconv"
	"time"

	"eigenflux_server/pkg/es"
	"eigenflux_server/pkg/logger"
)

// Item represents the item document in Elasticsearch
type Item struct {
	ID               int64                  `json:"id"`
	Content          string                 `json:"content"`
	Extra            map[string]interface{} `json:"extra"`
	RawURL           string                 `json:"raw_url,omitempty"`
	Summary          string                 `json:"summary"`
	Type             string                 `json:"type"` // supply, demand, info, alert
	Domains          []string               `json:"domains"`
	ExpireTime       *time.Time             `json:"expire_time,omitempty"`
	Geo              string                 `json:"geo,omitempty"`
	SourceType       string                 `json:"source_type"` // original, curated, forwarded
	ExpectedResponse string                 `json:"expected_response,omitempty"`
	Keywords         []string               `json:"keywords,omitempty"`
	GroupID          int64                  `json:"group_id,omitempty"`
	QualityScore     float64                `json:"quality_score,omitempty"`
	Lang             string                 `json:"lang,omitempty"`
	Timeliness       string                 `json:"timeliness,omitempty"`
	Embedding        []float32              `json:"embedding,omitempty"`
	CreatedAt        time.Time              `json:"created_at"`
	UpdatedAt        time.Time              `json:"updated_at"`
	Score            float64                `json:"-"` // ES _score, not part of the document
}

// IndexItem indexes an item document in Elasticsearch
func IndexItem(ctx context.Context, item *Item) error {
	body, err := json.Marshal(item)
	if err != nil {
		return fmt.Errorf("failed to marshal item: %w", err)
	}

	res, err := es.Client.Index(
		es.IndexName,
		bytes.NewReader(body),
		es.Client.Index.WithContext(ctx),
		es.Client.Index.WithDocumentID(fmt.Sprintf("%d", item.ID)),
	)
	if err != nil {
		return fmt.Errorf("failed to index item: %w", err)
	}
	defer res.Body.Close()

	if res.IsError() {
		return fmt.Errorf("ES returned error: %s", res.String())
	}

	return nil
}

// SearchItemsRequest represents the search request parameters
type SearchItemsRequest struct {
	Domains         []string // Domain tags matching
	Keywords        []string // Keyword matching
	Geo             string   // Geographic range fuzzy matching
	Limit           int      // Number of results to return
	FreshnessOffset string   // Gaussian decay offset (e.g. "12h")
	FreshnessScale  string   // Gaussian decay scale (e.g. "7d")
	FreshnessDecay  float64  // Gaussian decay factor (e.g. 0.8)
}

// SearchItemsResponse represents the search response
type SearchItemsResponse struct {
	Items      []Item
	NextCursor time.Time
	Total      int64
}

type esSearchResponse struct {
	Hits struct {
		Hits []struct {
			ID     string  `json:"_id"`
			Score  float64 `json:"_score"`
			Source Item    `json:"_source"`
		} `json:"hits"`
		Total struct {
			Value int64 `json:"value"`
		} `json:"total"`
	} `json:"hits"`
}

// SearchItems searches items based on domains, keywords, geo, and expire_time
func SearchItems(ctx context.Context, req *SearchItemsRequest) (*SearchItemsResponse, error) {
	if req.Limit <= 0 {
		req.Limit = 20
	}

	logger.Default().Debug("[ES] search request", "domains", req.Domains, "keywords", req.Keywords, "geo", req.Geo, "limit", req.Limit)

	// Build query
	query := buildSearchQuery(req)

	// Execute search
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(query); err != nil {
		return nil, fmt.Errorf("failed to encode query: %w", err)
	}

	res, err := es.Client.Search(
		es.Client.Search.WithContext(ctx),
		es.Client.Search.WithIndex(es.ReadIndexPattern),
		es.Client.Search.WithBody(&buf),
		es.Client.Search.WithTrackTotalHits(true),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to execute search: %w", err)
	}
	defer res.Body.Close()

	if res.IsError() {
		logger.Default().Error("ES search error", "response", res.String())
		return nil, fmt.Errorf("ES search error: %s", res.String())
	}

	parsed, err := parseSearchResponse(res.Body)
	if err != nil {
		return nil, err
	}

	logger.Default().Debug("ES search response", "hits", len(parsed.Hits.Hits), "total", parsed.Hits.Total.Value)

	items := make([]Item, 0, len(parsed.Hits.Hits))
	var nextCursor time.Time

	for i, hit := range parsed.Hits.Hits {
		item := hit.Source
		if hit.ID != "" {
			if id, err := strconv.ParseInt(hit.ID, 10, 64); err == nil {
				item.ID = id
			}
		}
		item.Score = hit.Score

		// Log first few items with scores for debugging
		if i < 5 {
			logger.Default().Debug("ES item", "index", i, "id", item.ID, "score", hit.Score, "updatedAt", item.UpdatedAt, "domains", item.Domains, "keywords", item.Keywords)
		}

		items = append(items, item)
		nextCursor = item.UpdatedAt
	}

	return &SearchItemsResponse{
		Items:      items,
		NextCursor: nextCursor,
		Total:      parsed.Hits.Total.Value,
	}, nil
}

func parseSearchResponse(r io.Reader) (*esSearchResponse, error) {
	var parsed esSearchResponse
	if err := json.NewDecoder(r).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &parsed, nil
}

// CountItems returns the total number of items in Elasticsearch
func CountItems(ctx context.Context) (int64, error) {
	query := map[string]interface{}{
		"query": map[string]interface{}{
			"match_all": map[string]interface{}{},
		},
	}

	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(query); err != nil {
		return 0, fmt.Errorf("failed to encode query: %w", err)
	}

	res, err := es.Client.Count(
		es.Client.Count.WithContext(ctx),
		es.Client.Count.WithIndex(es.ReadIndexPattern),
		es.Client.Count.WithBody(&buf),
	)
	if err != nil {
		return 0, fmt.Errorf("failed to execute count: %w", err)
	}
	defer res.Body.Close()

	if res.IsError() {
		return 0, fmt.Errorf("ES count error: %s", res.String())
	}

	var result map[string]interface{}
	if err := json.NewDecoder(res.Body).Decode(&result); err != nil {
		return 0, fmt.Errorf("failed to decode response: %w", err)
	}

	count := int64(result["count"].(float64))
	return count, nil
}
