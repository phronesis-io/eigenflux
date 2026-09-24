package discovery

import (
	"bytes"
	"context"
	"eigenflux_server/pkg/agentidentity"
	"eigenflux_server/pkg/agentindex"
	"eigenflux_server/pkg/commissionindex"
	"eigenflux_server/pkg/es"
	sortdal "eigenflux_server/rpc/sort/dal"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Decimal identity queries never fall through to fuzzy Agent retrieval, including
// unknown and overflowing IDs. Keep IDs as strings until the checked conversion.
func decimalAgentQuery(query string) bool {
	query = strings.TrimSpace(query)
	if query == "" {
		return false
	}
	for _, c := range query {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// Exact Agent search reads current identity fields, so it needs no ES backfill.
// Short IDs take precedence over names and retain their case-sensitive contract.
func (s *Source) exactAgents(ctx context.Context, c Context, limit int) ([]Document, error) {
	if limit < 1 || limit > 100 {
		return nil, fmt.Errorf("invalid Agent lookup limit")
	}
	query := strings.TrimSpace(c.Query)
	var ids []int64
	match := "name"
	if decimalAgentQuery(query) {
		id, err := strconv.ParseInt(query, 10, 64)
		if err != nil || id <= 0 {
			return []Document{}, nil
		}
		ids, match = []int64{id}, "agent_id"
	} else {
		if agentidentity.ValidShortID(query) {
			if err := s.DB.WithContext(ctx).Table("agents").Where("short_id = ?", query).Pluck("agent_id", &ids).Error; err != nil {
				return nil, err
			}
			if len(ids) > 0 {
				match = "short_id"
			}
		}
		if len(ids) == 0 {
			if err := s.DB.WithContext(ctx).Table("agents").Where("agent_name = ? OR agent_name_en = ?", query, query).
				Order("agent_id").Limit(limit).Pluck("agent_id", &ids).Error; err != nil {
				return nil, err
			}
		}
	}
	rows, err := agentindex.Load(ctx, s.DB, ids)
	if err != nil {
		return nil, err
	}
	out := make([]Document, 0, len(rows))
	for _, row := range rows {
		d := agentDocument(row)
		d.ExactMatch = match
		out = append(out, d)
	}
	return out, nil
}

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
func Query(c Context, k Kind, channel string, limit int) (map[string]any, error) {
	if limit < 1 || limit > 200 {
		return nil, fmt.Errorf("invalid retrieval limit")
	}
	filters, not := []any{}, []any{}
	author := "author_agent_id"
	textFields := []string{"content", "summary^2", "keywords.text"}
	lang := "lang"
	if k == Commission {
		author = "seller_agent_id"
		textFields = []string{"search_text", "title^2"}
		lang = "retrieval_slots.lang"
		filters = append(filters, term("active", true))
	}
	if k == Agent {
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
	if k == Commission {
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
	if k == Agent {
		body["_source"] = []string{"agent_id", "version", "projection_version"}
	}
	if k == Commission {
		body["_source"] = []string{"commission_id", "catalogue_version"}
	}
	switch channel {
	case "lexical":
		original := []any{map[string]any{"multi_match": map[string]any{"query": c.lexicalQuery(), "fields": textFields}}}
		// Existing analyzers need not fold width/Unicode identically. Retain the
		// caller's text as a parallel lexical clause until those indices migrate.
		if c.lexicalQuery() != c.Query {
			original = append(original, map[string]any{"multi_match": map[string]any{"query": c.Query, "fields": textFields}})
		}
		boolq["must"] = []any{map[string]any{"dis_max": map[string]any{"queries": original, "tie_breaker": 0}}}
		if c.QueryAnalysis != nil && len(c.QueryAnalysis.Phrases) > 0 {
			phrases := []any{}
			for _, phrase := range c.QueryAnalysis.Phrases {
				phrases = append(phrases, map[string]any{"multi_match": map[string]any{"query": phrase, "fields": textFields, "type": "phrase", "boost": 1.5}})
			}
			boolq["should"] = []any{map[string]any{"dis_max": map[string]any{"queries": phrases, "tie_breaker": 0}}}
		}
		body["query"] = map[string]any{"bool": boolq}
	case "synonym":
		if c.QueryAnalysis == nil || len(c.QueryAnalysis.Expansions) == 0 || len(c.QueryAnalysis.Expansions) > maxQueryExpansions {
			return nil, fmt.Errorf("missing or excessive query expansions")
		}
		variants := []any{}
		for _, expansion := range c.QueryAnalysis.Expansions {
			variants = append(variants, map[string]any{"bool": map[string]any{
				"must":   []any{map[string]any{"multi_match": map[string]any{"query": expansion.Query, "fields": textFields}}},
				"filter": []any{map[string]any{"multi_match": map[string]any{"query": expansion.To, "fields": textFields, "type": "phrase"}}},
				"boost":  0.5,
			}})
		}
		// dis_max prevents overlapping aliases from accumulating a score bonus.
		boolq["must"] = []any{map[string]any{"dis_max": map[string]any{"queries": variants, "tie_breaker": 0}}}
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
func (s *Source) search(ctx context.Context, c Context, k Kind, channel string, limit int) ([]Document, error) {
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
	if k == Agent {
		index = s.AgentIndex
	}
	if k == Commission {
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
				Index  string          `json:"_index"`
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
	out := []Document{}
	for _, h := range env.Hits.Hits {
		var d Document
		switch k {
		case Broadcast:
			var v sortdal.Item
			if err = json.Unmarshal(h.Source, &v); err != nil {
				return nil, err
			}
			d = broadcast(v)
		case Commission:
			var v commissionindex.Document
			if err = json.Unmarshal(h.Source, &v); err != nil {
				return nil, err
			}
			d = commission(v)
		case Agent:
			var v agentindex.Document
			if err = json.Unmarshal(h.Source, &v); err != nil {
				return nil, err
			}
			d = agentDocument(v)
		}
		if k != Broadcast {
			if h.Index == "" {
				return nil, fmt.Errorf("missing source index generation")
			}
			d.SourceIndex = h.Index
		}
		if channel == "lexical" || channel == "synonym" {
			d.Lexical = h.Score
		}
		out = append(out, d)
	}
	return out, nil
}
