package dal

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strconv"

	"eigenflux_server/pkg/es"
	"eigenflux_server/pkg/json"
	"eigenflux_server/pkg/logger"
)

// ServiceDoc represents a service document in Elasticsearch.
// Fields tagged `json:"-"` are NOT indexed in ES; they are joined in from
// trading_service_stats at rank time. See SearchServices for the merge step.
type ServiceDoc struct {
	ServiceID          int64     `json:"service_id"`
	SellerAgentID      int64     `json:"seller_agent_id"`
	Title              string    `json:"title"`
	CapabilityDesc     string    `json:"capability_desc"`
	CallSpecText       string    `json:"call_spec_text"`
	Keywords           []string  `json:"keywords,omitempty"`
	Domains            []string  `json:"domains,omitempty"`
	Embedding          []float32 `json:"embedding,omitempty"`
	AmountAtomic       int64     `json:"amount_atomic"`
	Asset              string    `json:"asset"`
	DeliveryDeadlineMs int64     `json:"delivery_deadline_ms"`
	UpdatedAt          int64     `json:"updated_at"`

	// LLM-enriched fields populated by ServiceConsumer.
	CapabilityTags []string  `json:"capability_tags,omitempty"`
	UseCases       string    `json:"use_cases,omitempty"`
	UsageEmbedding []float32 `json:"usage_embedding,omitempty"`

	// Stats joined from trading_service_stats; never written to ES.
	SuccessRate    float64 `json:"-"`
	AvgLatencyMs   int64   `json:"-"`
	OrderCount     int32   `json:"-"`
	ReleasedCount  int32   `json:"-"`
	RefundedCount  int32   `json:"-"`
	ExpiredCount   int32   `json:"-"`
	LastActivityAt int64   `json:"-"`

	Score float64 `json:"-"` // ES _score
}

type esServicesSearchResponse struct {
	Hits struct {
		Hits []struct {
			ID     string     `json:"_id"`
			Score  float64    `json:"_score"`
			Source ServiceDoc `json:"_source"`
		} `json:"hits"`
		Total struct {
			Value int64 `json:"value"`
		} `json:"total"`
	} `json:"hits"`
}

// SearchServices searches services based on query text and domain filters
func SearchServices(ctx context.Context, req *SearchServicesRequest) (*SearchServicesResponse, error) {
	if req.Limit <= 0 {
		req.Limit = 20
	}

	logger.Default().Debug("[ES] services search request", "query", req.Query, "domains", req.Domains, "limit", req.Limit)

	query := buildServiceSearchQuery(req)

	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(query); err != nil {
		return nil, fmt.Errorf("failed to encode query: %w", err)
	}

	res, err := es.Client.Search(
		es.Client.Search.WithContext(ctx),
		es.Client.Search.WithIndex(es.ServicesReadPattern),
		es.Client.Search.WithBody(&buf),
		es.Client.Search.WithTrackTotalHits(true),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to execute search: %w", err)
	}
	defer res.Body.Close()

	if res.IsError() {
		logger.Default().Error("ES services search error", "response", res.String())
		return nil, fmt.Errorf("ES search error: %s", res.String())
	}

	parsed, err := parseServicesSearchResponse(res.Body)
	if err != nil {
		return nil, err
	}

	logger.Default().Debug("ES services search response", "hits", len(parsed.Hits.Hits), "total", parsed.Hits.Total.Value)

	services := make([]ServiceDoc, 0, len(parsed.Hits.Hits))
	for _, hit := range parsed.Hits.Hits {
		svc := hit.Source
		if hit.ID != "" {
			if id, err := strconv.ParseInt(hit.ID, 10, 64); err == nil {
				svc.ServiceID = id
			}
		}
		svc.Score = hit.Score
		services = append(services, svc)
	}

	return &SearchServicesResponse{
		Services: services,
		Total:    parsed.Hits.Total.Value,
	}, nil
}

// SearchServicesByEmbedding performs a kNN recall query against the services
// index using an embedding vector. Distinct from SearchByEmbedding (items).
func SearchServicesByEmbedding(ctx context.Context, embedding []float32, k, numCandidates int) ([]ServiceDoc, error) {
	return execServiceKNN(ctx, "embedding", embedding, k, numCandidates)
}

// SearchServicesByUsageEmbedding mirrors SearchServicesByEmbedding but queries
// the usage_embedding field (task-side semantics) — used by SearchServices
// where each sub-intent's QueryText is embedded and matched against the LLM-
// rewritten use_cases representation of every service.
func SearchServicesByUsageEmbedding(ctx context.Context, embedding []float32, k, numCandidates int) ([]ServiceDoc, error) {
	return execServiceKNN(ctx, "usage_embedding", embedding, k, numCandidates)
}

func execServiceKNN(ctx context.Context, field string, embedding []float32, k, numCandidates int) ([]ServiceDoc, error) {
	if k <= 0 {
		k = 50
	}
	if numCandidates <= 0 {
		numCandidates = 200
	}

	knnClause := map[string]interface{}{
		"field":          field,
		"query_vector":   embedding,
		"k":              k,
		"num_candidates": numCandidates,
		"filter": map[string]interface{}{
			"term": map[string]interface{}{
				"status": "active",
			},
		},
	}

	query := map[string]interface{}{
		"knn": knnClause,
	}

	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(query); err != nil {
		return nil, fmt.Errorf("encode kNN query: %w", err)
	}

	res, err := es.Client.Search(
		es.Client.Search.WithContext(ctx),
		es.Client.Search.WithIndex(es.ServicesReadPattern),
		es.Client.Search.WithBody(&buf),
		es.Client.Search.WithTrackTotalHits(true),
	)
	if err != nil {
		return nil, fmt.Errorf("execute kNN search: %w", err)
	}
	defer res.Body.Close()

	if res.IsError() {
		return nil, fmt.Errorf("ES kNN search error: %s", res.String())
	}

	parsed, err := parseServicesSearchResponse(res.Body)
	if err != nil {
		return nil, err
	}

	services := make([]ServiceDoc, 0, len(parsed.Hits.Hits))
	for _, hit := range parsed.Hits.Hits {
		svc := hit.Source
		if hit.ID != "" {
			if id, parseErr := strconv.ParseInt(hit.ID, 10, 64); parseErr == nil {
				svc.ServiceID = id
			}
		}
		svc.Score = hit.Score
		services = append(services, svc)
	}

	logger.Ctx(ctx).Debug("kNN services recall", "k", k, "numCandidates", numCandidates, "returned", len(services))
	return services, nil
}

// FetchServiceByID retrieves a single service document from ES by service ID
func FetchServiceByID(ctx context.Context, serviceID int64) (*ServiceDoc, error) {
	query := map[string]interface{}{
		"query": map[string]interface{}{
			"term": map[string]interface{}{
				"service_id": serviceID,
			},
		},
		"size": 1,
	}

	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(query); err != nil {
		return nil, fmt.Errorf("encode fetch-by-id query: %w", err)
	}

	res, err := es.Client.Search(
		es.Client.Search.WithContext(ctx),
		es.Client.Search.WithIndex(es.ServicesReadPattern),
		es.Client.Search.WithBody(&buf),
	)
	if err != nil {
		return nil, fmt.Errorf("execute fetch-by-id search: %w", err)
	}
	defer res.Body.Close()

	if res.IsError() {
		return nil, fmt.Errorf("ES fetch-by-id error: %s", res.String())
	}

	parsed, err := parseServicesSearchResponse(res.Body)
	if err != nil {
		return nil, err
	}

	if len(parsed.Hits.Hits) == 0 {
		return nil, nil
	}

	hit := parsed.Hits.Hits[0]
	svc := hit.Source
	if hit.ID != "" {
		if id, parseErr := strconv.ParseInt(hit.ID, 10, 64); parseErr == nil {
			svc.ServiceID = id
		}
	}
	svc.Score = hit.Score
	return &svc, nil
}

func parseServicesSearchResponse(r io.Reader) (*esServicesSearchResponse, error) {
	var parsed esServicesSearchResponse
	if err := json.NewDecoder(r).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &parsed, nil
}

// IntentRecall is one fan-out lane: one sub-intent's query text + its
// pre-computed embedding. Embedding may be nil — in that case the lane
// falls back to BM25 only.
type IntentRecall struct {
	Name      string
	QueryText string
	Embedding []float32
	Filters   *SearchServicesFilters // shared per-request price/deadline caps
}

// SearchServicesFilters are the request-level filters threaded through every lane.
type SearchServicesFilters struct {
	MaxPriceAtomic int64
	DeadlineMsMax  int64
}

// IntentMatch wraps a ServiceDoc with the union of intents that recalled it.
type IntentMatch struct {
	ServiceDoc
	MatchedIntents []string
}

// SearchServicesByIntents fans out kNN + BM25 per intent in parallel against
// the services index, then merges by service_id with MatchedIntents unioned.
// perIntentCap defaults to 50 when <= 0. Score per merged candidate is the
// MAX across lanes (caller applies importance weighting on top).
//
// Lane failures are logged and skipped; the function does not return an error
// unless every lane fails AND no candidates were collected.
func SearchServicesByIntents(ctx context.Context, intents []IntentRecall, perIntentCap int) ([]IntentMatch, error) {
	if perIntentCap <= 0 {
		perIntentCap = 50
	}
	if len(intents) == 0 {
		return nil, nil
	}

	type laneResult struct {
		name string
		docs []ServiceDoc
		ok   bool
	}
	ch := make(chan laneResult, len(intents))

	for _, in := range intents {
		in := in
		go func() {
			knn, knnErr := searchByUsageOrSkip(ctx, in.Embedding, perIntentCap)
			bm25Docs, bm25Err := searchByBM25(ctx, in, perIntentCap)
			merged := mergeRecallLanes(knn, bm25Docs)
			ok := knnErr == nil || bm25Err == nil
			if knnErr != nil {
				logger.Default().Warn("SearchServicesByIntents kNN lane failed", "intent", in.Name, "err", knnErr)
			}
			if bm25Err != nil {
				logger.Default().Warn("SearchServicesByIntents BM25 lane failed", "intent", in.Name, "err", bm25Err)
			}
			ch <- laneResult{name: in.Name, docs: merged, ok: ok}
		}()
	}

	byID := make(map[int64]*IntentMatch, len(intents)*perIntentCap)
	successes := 0
	for range intents {
		r := <-ch
		if r.ok {
			successes++
		}
		for _, d := range r.docs {
			existing, ok := byID[d.ServiceID]
			if !ok {
				m := IntentMatch{ServiceDoc: d, MatchedIntents: []string{r.name}}
				byID[d.ServiceID] = &m
				continue
			}
			existing.MatchedIntents = appendUniqueIntent(existing.MatchedIntents, r.name)
			if d.Score > existing.Score {
				existing.Score = d.Score
			}
		}
	}

	if successes == 0 && len(byID) == 0 {
		return nil, fmt.Errorf("SearchServicesByIntents: all lanes failed")
	}

	out := make([]IntentMatch, 0, len(byID))
	for _, m := range byID {
		out = append(out, *m)
	}
	return out, nil
}

func searchByUsageOrSkip(ctx context.Context, emb []float32, k int) ([]ServiceDoc, error) {
	if emb == nil {
		return nil, nil // BM25-only lane; not an error
	}
	return SearchServicesByUsageEmbedding(ctx, emb, k, k*4)
}

func searchByBM25(ctx context.Context, in IntentRecall, k int) ([]ServiceDoc, error) {
	req := &SearchServicesRequest{
		Query: in.QueryText,
		Limit: k,
	}
	if in.Filters != nil {
		req.MaxPriceAtomic = in.Filters.MaxPriceAtomic
		req.DeadlineMsMax = in.Filters.DeadlineMsMax
	}
	resp, err := SearchServices(ctx, req)
	if err != nil {
		return nil, err
	}
	return resp.Services, nil
}

// mergeRecallLanes dedups doc lists by ServiceID, preferring the first occurrence.
func mergeRecallLanes(a, b []ServiceDoc) []ServiceDoc {
	if len(a) == 0 {
		return b
	}
	if len(b) == 0 {
		return a
	}
	seen := make(map[int64]struct{}, len(a)+len(b))
	out := make([]ServiceDoc, 0, len(a)+len(b))
	for _, d := range a {
		if _, dup := seen[d.ServiceID]; dup {
			continue
		}
		seen[d.ServiceID] = struct{}{}
		out = append(out, d)
	}
	for _, d := range b {
		if _, dup := seen[d.ServiceID]; dup {
			continue
		}
		seen[d.ServiceID] = struct{}{}
		out = append(out, d)
	}
	return out
}

func appendUniqueIntent(s []string, v string) []string {
	for _, x := range s {
		if x == v {
			return s
		}
	}
	return append(s, v)
}
