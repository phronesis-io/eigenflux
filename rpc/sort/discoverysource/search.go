package discoverysource

import (
	"bytes"
	"context"
	"eigenflux_server/pkg/agentindex"
	"eigenflux_server/pkg/commissionindex"
	"eigenflux_server/pkg/discovery"
	"eigenflux_server/pkg/es"
	sortdal "eigenflux_server/rpc/sort/dal"
	"encoding/json"
	"fmt"
	"io"
)

func term(field string, value any) map[string]any {
	return map[string]any{"term": map[string]any{field: value}}
}
func terms(field string, value any) map[string]any {
	return map[string]any{"terms": map[string]any{field: value}}
}
func rangeFilter(field, op string, value any) map[string]any {
	return map[string]any{"range": map[string]any{field: map[string]any{op: value}}}
}

// Query pushes positive, indexable hard constraints into every channel. The
// authoritative evaluator rechecks the complete contract after retrieval.
func Query(c discovery.Context, k discovery.Kind, channel string, limit int) (map[string]any, error) {
	if limit < 1 || limit > 200 {
		return nil, fmt.Errorf("invalid retrieval limit")
	}
	filters, not := []any{}, []any{}
	author := "author_agent_id"
	textFields := []string{"content", "summary^2", "keywords.text"}
	lang := "lang"
	if k == discovery.Commission {
		author = "seller_agent_id"
		textFields = []string{"search_text", "title^2"}
		lang = "retrieval_slots.lang"
		filters = append(filters, term("active", true))
	}
	if k == discovery.Agent {
		author = "agent_id"
		textFields = []string{"search_text", "display_name^2"}
		lang = "retrieval_slots.lang"
		filters = append(filters, term("active", true))
	}
	f := c.Filters
	not = append(not, terms(author, f.ExcludeAuthors))
	for field, value := range map[string]string{"category": f.Category, "subtype": f.Subtype, "taxonomy_version": f.TaxonomyVersion} {
		if value != "" {
			filters = append(filters, term("retrieval_slots."+field, value))
		}
	}
	if len(f.Intents) > 0 {
		filters = append(filters, terms("retrieval_slots.intents", f.Intents))
	}
	if len(f.Lang) > 0 {
		filters = append(filters, terms(lang, f.Lang))
	}
	if len(f.ProviderRegion) > 0 {
		filters = append(filters, terms("retrieval_slots.provider_region", f.ProviderRegion))
	}
	if k == discovery.Commission {
		if f.Currency != "" {
			filters = append(filters, term("currency", f.Currency))
		}
		for _, r := range []struct {
			field, op string
			v         *int64
		}{{"price_fen", "gte", f.MinPriceFen}, {"price_fen", "lte", f.MaxPriceFen}, {"price_fen", "lte", f.BudgetMaxFen}, {"promised_delivery_ms", "gte", f.MinDurationMS}, {"promised_delivery_ms", "lte", f.MaxDurationMS}} {
			if r.v != nil {
				filters = append(filters, rangeFilter(r.field, r.op, *r.v))
			}
		}
		if f.DeadlineMS != nil {
			filters = append(filters, rangeFilter("promised_delivery_ms", "lte", *f.DeadlineMS-c.CreatedAt))
		}
	}
	boolq := map[string]any{"filter": filters, "must_not": not}
	body := map[string]any{"size": limit, "track_total_hits": false}
	switch channel {
	case "lexical":
		boolq["must"] = []any{map[string]any{"multi_match": map[string]any{"query": c.Query, "fields": textFields}}}
		body["query"] = map[string]any{"bool": boolq}
	case "dense":
		if len(c.Vector) == 0 {
			return nil, fmt.Errorf("missing query vector")
		}
		body["knn"] = map[string]any{"field": "embedding", "query_vector": c.Vector, "k": limit, "num_candidates": limit * 3, "filter": map[string]any{"bool": boolq}}
	case "structured":
		if len(c.SoftIntents) > 0 {
			boolq["should"] = []any{terms("retrieval_slots.intents", c.SoftIntents)}
			boolq["minimum_should_match"] = 1
		} else if f.Category == "" {
			return nil, fmt.Errorf("missing structured evidence")
		}
		body["query"] = map[string]any{"bool": boolq}
	default:
		return nil, fmt.Errorf("unsupported channel")
	}
	return body, nil
}
func (s *Source) search(ctx context.Context, c discovery.Context, k discovery.Kind, channel string, limit int) ([]discovery.Document, error) {
	q, err := Query(c, k, channel, limit)
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(q)
	if err != nil {
		return nil, err
	}
	index := s.BroadcastIndex
	if index == "" {
		index = es.ReadIndexPattern
	}
	if k == discovery.Agent {
		index = s.AgentIndex
	}
	if k == discovery.Commission {
		index = s.CommissionIndex
	}
	if index == "" || es.Client == nil {
		return nil, fmt.Errorf("source index unavailable")
	}
	resp, err := es.Client.Search(es.Client.Search.WithContext(ctx), es.Client.Search.WithIndex(index), es.Client.Search.WithBody(bytes.NewReader(b)))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.IsError() {
		return nil, fmt.Errorf("source index read failed: %d", resp.StatusCode)
	}
	var env struct {
		TimedOut bool `json:"timed_out"`
		Shards   struct {
			Failed int `json:"failed"`
		} `json:"_shards"`
		Hits struct {
			Hits []struct {
				Score  float64         `json:"_score"`
				Source json.RawMessage `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&env); err != nil {
		return nil, err
	}
	if env.TimedOut || env.Shards.Failed > 0 {
		return nil, fmt.Errorf("incomplete source search")
	}
	out := []discovery.Document{}
	for _, h := range env.Hits.Hits {
		var d discovery.Document
		switch k {
		case discovery.Broadcast:
			var v sortdal.Item
			if err = json.Unmarshal(h.Source, &v); err != nil {
				return nil, err
			}
			d = broadcast(v)
		case discovery.Commission:
			var v commissionindex.Document
			if err = json.Unmarshal(h.Source, &v); err != nil {
				return nil, err
			}
			d = commission(v)
		case discovery.Agent:
			var v agentindex.Document
			if err = json.Unmarshal(h.Source, &v); err != nil {
				return nil, err
			}
			d = v.Candidate()
		}
		if channel == "lexical" {
			d.Lexical = h.Score
		}
		out = append(out, d)
	}
	return out, nil
}
