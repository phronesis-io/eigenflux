package commissionindex

import (
	"bytes"
	"context"
	"eigenflux_server/pkg/es"
	"eigenflux_server/pkg/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
)

type ESStore struct {
	Index      string
	Alias      string
	Dimensions int
}

func (s ESStore) readIndex() string {
	if s.Alias != "" {
		return s.Alias
	}
	return s.Index
}

func (s ESStore) Ensure(ctx context.Context) error {
	if es.Client == nil {
		return fmt.Errorf("Elasticsearch client is not initialized")
	}
	body, err := json.Marshal(map[string]any{"settings": map[string]any{"number_of_shards": 1, "number_of_replicas": 0}, "mappings": Mapping(s.Dimensions), "aliases": map[string]any{s.Alias: map[string]any{}}})
	if err != nil {
		return err
	}
	res, err := es.Client.Indices.Create(s.Index, es.Client.Indices.Create.WithContext(ctx), es.Client.Indices.Create.WithBody(bytes.NewReader(body)))
	if err != nil {
		return fmt.Errorf("create Commission index: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusBadRequest {
		return nil
	}
	if res.IsError() {
		return fmt.Errorf("create Commission index: %s", res.String())
	}
	return nil
}

func Mapping(dims int) map[string]any {
	return map[string]any{"properties": map[string]any{
		"commission_id": map[string]any{"type": "long"}, "seller_agent_id": map[string]any{"type": "long"}, "active": map[string]any{"type": "boolean"},
		"catalogue_version": map[string]any{"type": "long"}, "statistics_version": map[string]any{"type": "long"}, "title": map[string]any{"type": "text"},
		"capability_description": map[string]any{"type": "text"}, "request_spec_text": map[string]any{"type": "text"}, "delivery_spec_text": map[string]any{"type": "text"},
		"tags": map[string]any{"type": "keyword"}, "search_text": map[string]any{"type": "text"}, "price_fen": map[string]any{"type": "long"},
		"currency": map[string]any{"type": "keyword"}, "promised_delivery_ms": map[string]any{"type": "long"}, "completed_count": map[string]any{"type": "long"},
		"refunded_count": map[string]any{"type": "long"}, "completion_rate_bps": map[string]any{"type": "integer"}, "average_rating_milli": map[string]any{"type": "integer"},
		"has_rating": map[string]any{"type": "boolean"}, "average_delivery_ms": map[string]any{"type": "long"}, "updated_at": map[string]any{"type": "date"},
		"embedding": map[string]any{"type": "dense_vector", "dims": dims, "index": true, "similarity": "cosine"},
	}}
}

func (s ESStore) Upsert(ctx context.Context, doc Document) error {
	body, err := json.Marshal(map[string]any{"scripted_upsert": true, "script": map[string]any{"lang": "painless", "source": "if (ctx.op == 'create') { ctx._source = params.doc; } else { if (params.doc.catalogue_version >= ctx._source.catalogue_version) { for (entry in params.doc.entrySet()) { if (entry.getKey() != 'statistics_version' && entry.getKey() != 'completed_count' && entry.getKey() != 'refunded_count' && entry.getKey() != 'completion_rate_bps' && entry.getKey() != 'average_rating_milli' && entry.getKey() != 'has_rating' && entry.getKey() != 'average_delivery_ms') { ctx._source[entry.getKey()] = entry.getValue(); } } } if (params.doc.statistics_version >= ctx._source.statistics_version) { ctx._source.statistics_version = params.doc.statistics_version; ctx._source.completed_count = params.doc.completed_count; ctx._source.refunded_count = params.doc.refunded_count; ctx._source.completion_rate_bps = params.doc.completion_rate_bps; ctx._source.average_rating_milli = params.doc.average_rating_milli; ctx._source.has_rating = params.doc.has_rating; ctx._source.average_delivery_ms = params.doc.average_delivery_ms; } }", "params": map[string]any{"doc": doc}}, "upsert": doc})
	if err != nil {
		return fmt.Errorf("marshal Commission update: %w", err)
	}
	path := "/" + s.readIndex() + "/_update/" + strconv.FormatInt(doc.CommissionID, 10)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	res, err := es.Client.Perform(req)
	if err != nil {
		return fmt.Errorf("update Commission index: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(res.Body)
		return fmt.Errorf("update Commission index: %s", body)
	}
	return nil
}

func (s ESStore) Search(ctx context.Context, req SearchRequest) ([]Hit, error) {
	if req.Limit <= 0 {
		req.Limit = 20
	}
	filters := []any{map[string]any{"term": map[string]any{"active": true}}}
	for field, r := range map[string][2]int64{"price_fen": {req.MinPriceFen, req.MaxPriceFen}, "promised_delivery_ms": {req.MinDurationMS, req.MaxDurationMS}} {
		if r[0] > 0 || r[1] > 0 {
			bounds := map[string]any{}
			if r[0] > 0 {
				bounds["gte"] = r[0]
			}
			if r[1] > 0 {
				bounds["lte"] = r[1]
			}
			filters = append(filters, map[string]any{"range": map[string]any{field: bounds}})
		}
	}
	query := map[string]any{"size": req.Limit, "query": map[string]any{"bool": map[string]any{"filter": filters, "must": []any{map[string]any{"multi_match": map[string]any{"query": req.Query, "fields": []string{"title^3", "capability_description^2", "request_spec_text", "delivery_spec_text", "search_text"}}}}}}}
	if len(req.Embedding) > 0 {
		query["knn"] = map[string]any{"field": "embedding", "query_vector": req.Embedding, "k": req.Limit, "num_candidates": req.Limit * 4, "filter": map[string]any{"bool": map[string]any{"filter": filters}}}
	}
	body, err := json.Marshal(query)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "/"+s.readIndex()+"/_search", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	response, err := es.Client.Perform(request)
	if err != nil {
		return nil, fmt.Errorf("search Commission index: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(response.Body)
		return nil, fmt.Errorf("search Commission index: %s", body)
	}
	var decoded struct {
		Hits struct {
			Hits []struct {
				Score  float64  `json:"_score"`
				Source Document `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		return nil, err
	}
	hits := make([]Hit, 0, len(decoded.Hits.Hits))
	for _, hit := range decoded.Hits.Hits {
		hits = append(hits, Hit{Document: hit.Source, KeywordScore: hit.Score, SemanticScore: hit.Score})
	}
	return hits, nil
}
