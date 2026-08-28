package commissionindex

import (
	"bytes"
	"context"
	"eigenflux_server/pkg/es"
	"eigenflux_server/pkg/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
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

var errCommissionIndexRead = errors.New("commission index read failed")

const maxCommissionGetResponseBytes = 64 << 10
const maxCommissionSearchResponseBytes = 2 << 20

func (s ESStore) Get(ctx context.Context, commissionID int64) (Document, bool, error) {
	if commissionID <= 0 || es.Client == nil || s.readIndex() == "" {
		return Document{}, false, errCommissionIndexRead
	}
	path := "/" + url.PathEscape(s.readIndex()) + "/_doc/" + strconv.FormatInt(commissionID, 10)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return Document{}, false, errCommissionIndexRead
	}
	query := req.URL.Query()
	query.Set("_source_includes", "commission_id,active,catalogue_version,statistics_version")
	req.URL.RawQuery = query.Encode()
	res, err := es.Client.Perform(req)
	if err != nil {
		if res != nil && res.Body != nil {
			_ = res.Body.Close()
		}
		return Document{}, false, errCommissionIndexRead
	}
	if res == nil || res.Body == nil {
		return Document{}, false, errCommissionIndexRead
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return decodeDocumentMiss(res.Body, commissionID)
	}
	if res.StatusCode != http.StatusOK {
		return Document{}, false, errCommissionIndexRead
	}

	limited := &io.LimitedReader{R: res.Body, N: maxCommissionGetResponseBytes + 1}
	decoder := json.NewDecoder(limited)
	var envelope struct {
		Index  string `json:"_index"`
		ID     string `json:"_id"`
		Found  *bool  `json:"found"`
		Source struct {
			CommissionID      int64 `json:"commission_id"`
			Active            bool  `json:"active"`
			CatalogueVersion  int64 `json:"catalogue_version"`
			StatisticsVersion int64 `json:"statistics_version"`
		} `json:"_source"`
	}
	if err := decoder.Decode(&envelope); err != nil || limited.N == 0 || envelope.Index == "" ||
		envelope.ID != strconv.FormatInt(commissionID, 10) || envelope.Found == nil || !*envelope.Found ||
		envelope.Source.CommissionID != commissionID {
		return Document{}, false, errCommissionIndexRead
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Document{}, false, errCommissionIndexRead
	}
	return Document{
		CommissionID:      envelope.Source.CommissionID,
		Active:            envelope.Source.Active,
		CatalogueVersion:  envelope.Source.CatalogueVersion,
		StatisticsVersion: envelope.Source.StatisticsVersion,
	}, true, nil
}

func decodeDocumentMiss(body io.Reader, commissionID int64) (Document, bool, error) {
	limited := &io.LimitedReader{R: body, N: maxCommissionGetResponseBytes + 1}
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	var envelope struct {
		Index  string          `json:"_index"`
		ID     string          `json:"_id"`
		Found  *bool           `json:"found"`
		Error  json.RawMessage `json:"error,omitempty"`
		Status int             `json:"status,omitempty"`
	}
	if err := decoder.Decode(&envelope); err != nil || limited.N == 0 || envelope.Index == "" ||
		envelope.ID != strconv.FormatInt(commissionID, 10) || envelope.Found == nil || *envelope.Found ||
		len(envelope.Error) != 0 || envelope.Status != 0 {
		return Document{}, false, errCommissionIndexRead
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Document{}, false, errCommissionIndexRead
	}
	return Document{}, false, nil
}

var errCommissionIndexMapping = errors.New("commission index mapping unavailable")

func (s ESStore) Ready(ctx context.Context) (int, error) {
	if es.Client == nil || s.readIndex() == "" {
		return 0, errCommissionIndexMapping
	}
	path := "/" + url.PathEscape(s.readIndex()) + "/_mapping"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	if err != nil {
		return 0, errCommissionIndexMapping
	}
	res, err := es.Client.Perform(req)
	if err != nil {
		if res != nil && res.Body != nil {
			_ = res.Body.Close()
		}
		return 0, errCommissionIndexMapping
	}
	if res == nil || res.Body == nil {
		return 0, errCommissionIndexMapping
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return 0, errCommissionIndexMapping
	}
	limited := &io.LimitedReader{R: res.Body, N: maxCommissionGetResponseBytes + 1}
	decoder := json.NewDecoder(limited)
	var mappings map[string]struct {
		Mappings struct {
			Properties map[string]struct {
				Type string `json:"type"`
				Dims int    `json:"dims"`
			} `json:"properties"`
		} `json:"mappings"`
	}
	if err := decoder.Decode(&mappings); err != nil || limited.N == 0 || len(mappings) == 0 {
		return 0, errCommissionIndexMapping
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return 0, errCommissionIndexMapping
	}
	dimensions := 0
	for _, mapping := range mappings {
		embedding, ok := mapping.Mappings.Properties["embedding"]
		if !ok || embedding.Type != "dense_vector" || embedding.Dims <= 0 || dimensions != 0 && dimensions != embedding.Dims {
			return 0, errCommissionIndexMapping
		}
		dimensions = embedding.Dims
	}
	if dimensions == 0 {
		return 0, errCommissionIndexMapping
	}
	return dimensions, nil
}

func (s ESStore) Ensure(ctx context.Context) error {
	if es.Client == nil {
		return fmt.Errorf("Elasticsearch client is not initialized")
	}
	body, err := json.Marshal(map[string]any{"settings": map[string]any{"number_of_shards": 1, "number_of_replicas": 0}, "mappings": Mapping(s.Dimensions)})
	if err != nil {
		return err
	}
	res, err := es.Client.Indices.Create(s.Index, es.Client.Indices.Create.WithContext(ctx), es.Client.Indices.Create.WithBody(bytes.NewReader(body)))
	if err != nil {
		return fmt.Errorf("create Commission index: %w", err)
	}
	status := res.StatusCode
	_ = res.Body.Close()
	if status != http.StatusBadRequest && status >= http.StatusMultipleChoices {
		return fmt.Errorf("create Commission index: HTTP %d", status)
	}
	if strings.TrimSpace(s.Alias) == "" {
		return nil
	}
	aliasResponse, err := es.Client.Indices.GetAlias(
		es.Client.Indices.GetAlias.WithContext(ctx),
		es.Client.Indices.GetAlias.WithName(s.Alias),
	)
	if err != nil {
		return fmt.Errorf("check Commission index alias: %w", err)
	}
	status = aliasResponse.StatusCode
	_ = aliasResponse.Body.Close()
	if status == http.StatusOK {
		return nil
	}
	if status != http.StatusNotFound {
		return fmt.Errorf("check Commission index alias: HTTP %d", status)
	}
	return s.PromoteAlias(ctx)
}

// PromoteAlias atomically makes the populated concrete index the sole alias target.
func (s ESStore) PromoteAlias(ctx context.Context) error {
	if es.Client == nil || strings.TrimSpace(s.Index) == "" || strings.TrimSpace(s.Alias) == "" {
		return fmt.Errorf("Commission index alias configuration is invalid")
	}
	aliasBody, err := json.Marshal(map[string]any{"actions": []any{
		map[string]any{"remove": map[string]any{"index": "*", "alias": s.Alias, "must_exist": false}},
		map[string]any{"add": map[string]any{"index": s.Index, "alias": s.Alias, "is_write_index": true}},
	}})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "/_aliases", bytes.NewReader(aliasBody))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	aliasResponse, err := es.Client.Perform(request)
	if err != nil {
		return fmt.Errorf("switch Commission index alias: %w", err)
	}
	defer aliasResponse.Body.Close()
	if aliasResponse.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("switch Commission index alias: HTTP %d", aliasResponse.StatusCode)
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
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := es.Client.Perform(req)
	if err != nil {
		return fmt.Errorf("update Commission index: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("update Commission index: HTTP %d", res.StatusCode)
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
	query := map[string]any{
		"size":    req.Limit,
		"_source": []string{"commission_id", "completion_rate_bps", "average_rating_milli", "has_rating", "completed_count"},
		"query":   map[string]any{"bool": map[string]any{"filter": filters, "must": []any{map[string]any{"multi_match": map[string]any{"query": req.Query, "fields": []string{"title^3", "capability_description^2", "request_spec_text", "delivery_spec_text", "search_text"}}}}}},
	}
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
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := es.Client.Perform(request)
	if err != nil {
		return nil, fmt.Errorf("search Commission index: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("search Commission index: HTTP %d", response.StatusCode)
	}
	var decoded struct {
		Hits struct {
			Hits []struct {
				Score  float64  `json:"_score"`
				Source Document `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	limited := &io.LimitedReader{R: response.Body, N: maxCommissionSearchResponseBytes + 1}
	decoder := json.NewDecoder(limited)
	if err := decoder.Decode(&decoded); err != nil || limited.N == 0 {
		return nil, fmt.Errorf("decode Commission search response")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("decode Commission search response")
	}
	hits := make([]Hit, 0, len(decoded.Hits.Hits))
	for _, hit := range decoded.Hits.Hits {
		hits = append(hits, Hit{Document: hit.Source, KeywordScore: hit.Score, SemanticScore: hit.Score})
	}
	return hits, nil
}
