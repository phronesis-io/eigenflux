package main

import (
	"context"
	"strings"

	"eigenflux_server/kitex_gen/eigenflux/sort"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/json"
	"eigenflux_server/pkg/logger"
	sortDal "eigenflux_server/rpc/sort/dal"
	"eigenflux_server/rpc/sort/rank"
	"eigenflux_server/rpc/sort/rerank"
	"eigenflux_server/rpc/sort/serviceranker"
	tradeDal "eigenflux_server/rpc/trade/dal"
)

const (
	entryTypeItem    = "item"
	entryTypeService = "service"

	// serviceRecallSourceName is the only value emitted today; the service
	// stream comes exclusively from the services-* ES index. Kept as a
	// constant so a future kNN/two-tower service recall channel can extend
	// the schema with another value without churning callers.
	serviceRecallSourceName = "service_es"
)

// mixServicesIntoFeed augments the item-only top-N (itemIDs / sortedItems) with
// services pulled from the trade catalogue, ranked by serviceranker and merged
// via the rerank policy chain. The contract: when at least one active service
// is recalled, the rerank guarantees one appears inside the top-`limit`
// positions (tail-replacement via BoundsPolicy.Floor:1).
//
// Returns the rewritten itemIDs and sortedItems. When the feature flag is off
// or no services are recalled, the inputs are returned unchanged. Each emitted
// SortedItem carries the per-candidate rerank-stage observations on
// `item_features` (normalized_score, rerank_reasons, plus full service
// breakdown when entry_type=service) so the replay log can analyse mix
// decisions end-to-end.
func mixServicesIntoFeed(
	ctx context.Context,
	itemIDs []int64,
	sortedItems []*sort.SortedItem,
	keywords []string,
	domains []string,
	agentFeatures string,
	limit int,
	recallSize int,
) ([]int64, []*sort.SortedItem) {
	if limit <= 0 || len(sortedItems) == 0 {
		return itemIDs, sortedItems
	}

	services, err := recallServices(ctx, keywords, domains, recallSize)
	if err != nil {
		logger.Ctx(ctx).Warn("service mix: recall failed, returning items-only", "err", err)
		return itemIDs, sortedItems
	}
	if len(services) == 0 {
		logger.Ctx(ctx).Debug("service mix: no services recalled")
		return itemIDs, sortedItems
	}

	rankedServices := serviceranker.New(serviceRankerCfg).Rank(services, nil, recallSize)
	svcByID := make(map[int64]*sortDal.ServiceDoc, len(services))
	for i := range services {
		svcByID[services[i].ServiceID] = &services[i]
	}
	breakdownByID := make(map[int64]serviceranker.ServiceScoreBreakdown, len(rankedServices))
	for _, rs := range rankedServices {
		breakdownByID[rs.ServiceID] = rs.Breakdown
	}

	candidates := make([]rank.Candidate, 0, len(sortedItems)+len(rankedServices))
	for _, si := range sortedItems {
		candidates = append(candidates, rank.NewCandidate(
			si.ItemId, rank.CandidateItem, si.Score, nil, si,
		))
	}
	for _, rs := range rankedServices {
		doc := svcByID[rs.ServiceID]
		if doc == nil {
			continue
		}
		candidates = append(candidates, rank.NewCandidate(
			rs.ServiceID, rank.CandidateService, rs.Score, nil, doc,
		))
	}

	mixer := rerank.New(
		&rerank.DedupPolicy{},
		&rerank.NormalizePolicy{Method: rerank.MinMax},
		&rerank.BoundsPolicy{
			Limit: limit,
			Bounds: map[rank.CandidateType]rerank.Bound{
				rank.CandidateService: {Floor: 1},
			},
		},
	)
	final := mixer.Rerank(candidates, limit)

	outIDs := make([]int64, 0, len(final))
	outSorted := make([]*sort.SortedItem, 0, len(final))
	for _, c := range final {
		outIDs = append(outIDs, c.ID())

		var reasons []string
		if bc, ok := c.(*rank.BasicCandidate); ok {
			reasons = bc.Reasons()
		}
		normalizedScore := c.Score()

		switch c.Type() {
		case rank.CandidateItem:
			si, ok := c.Source().(*sort.SortedItem)
			if !ok {
				continue
			}
			cp := *si
			etype := entryTypeItem
			cp.EntryType = &etype
			cp.Score = normalizedScore
			existingFeat := ""
			if si.ItemFeatures != nil {
				existingFeat = *si.ItemFeatures
			}
			featStr := augmentItemFeaturesJSON(existingFeat, reasons, normalizedScore)
			cp.ItemFeatures = &featStr
			outSorted = append(outSorted, &cp)

		case rank.CandidateService:
			doc, ok := c.Source().(*sortDal.ServiceDoc)
			if !ok {
				continue
			}
			etype := entryTypeService
			featStr := buildServiceFeaturesJSON(doc, breakdownByID[c.ID()], reasons, normalizedScore)
			agentFeatCopy := agentFeatures
			outSorted = append(outSorted, &sort.SortedItem{
				ItemId:        c.ID(),
				Score:         normalizedScore,
				EntryType:     &etype,
				ItemFeatures:  &featStr,
				AgentFeatures: &agentFeatCopy,
			})
		}
	}

	logger.Ctx(ctx).Info("service mix applied",
		"recalled_services", len(services),
		"final_total", len(final),
		"final_services", countByType(final, rank.CandidateService),
	)
	return outIDs, outSorted
}

// augmentItemFeaturesJSON parses an existing item_features JSON string (built
// during the item-rank stage), appends the rerank-stage observations, and
// returns the re-serialised JSON. If the input is empty or malformed, returns
// a minimal JSON containing only the rerank-stage fields and entry_type.
func augmentItemFeaturesJSON(existing string, reasons []string, normalizedScore float64) string {
	feat := map[string]any{}
	if existing != "" {
		_ = json.Unmarshal([]byte(existing), &feat)
		if feat == nil {
			feat = map[string]any{}
		}
	}
	feat["entry_type"] = entryTypeItem
	feat["normalized_score"] = normalizedScore
	if len(reasons) > 0 {
		feat["rerank_reasons"] = reasons
	}
	out, err := json.Marshal(feat)
	if err != nil {
		return existing
	}
	return string(out)
}

// withRerankReasons merges rerank reason tags into an existing item_features
// JSON string, preserving any reasons already present. Returns the input
// unchanged on parse/serialize failure.
func withRerankReasons(existing string, reasons []string) string {
	if len(reasons) == 0 {
		return existing
	}
	feat := map[string]any{}
	if existing != "" {
		_ = json.Unmarshal([]byte(existing), &feat)
		if feat == nil {
			feat = map[string]any{}
		}
	}
	merged := reasons
	if prev, ok := feat["rerank_reasons"].([]any); ok {
		for _, p := range prev {
			if s, ok := p.(string); ok {
				merged = append(merged, s)
			}
		}
	}
	feat["rerank_reasons"] = merged
	out, err := json.Marshal(feat)
	if err != nil {
		return existing
	}
	return string(out)
}

// buildServiceFeaturesJSON constructs the item_features payload for a service
// candidate. The schema mirrors the item-features JSON where possible
// (entry_type, rank_scores, recall_source, rerank_reasons, normalized_score)
// and adds service-specific business fields plus a stats block. Replay log
// analysis treats both kinds uniformly.
func buildServiceFeaturesJSON(doc *sortDal.ServiceDoc, breakdown serviceranker.ServiceScoreBreakdown, reasons []string, normalizedScore float64) string {
	feat := map[string]any{
		"entry_type":           entryTypeService,
		"service_id":           doc.ServiceID,
		"seller_agent_id":      doc.SellerAgentID,
		"title":                doc.Title,
		"capability_desc":      doc.CapabilityDesc,
		"domains":              doc.Domains,
		"amount_atomic":        doc.AmountAtomic,
		"asset":                doc.Asset,
		"delivery_deadline_ms": doc.DeliveryDeadlineMs,
		"updated_at":           doc.UpdatedAt,
		"rank_scores":          breakdown,
		"stats": map[string]any{
			"success_rate":     doc.SuccessRate,
			"avg_latency_ms":   doc.AvgLatencyMs,
			"order_count":      doc.OrderCount,
			"released_count":   doc.ReleasedCount,
			"refunded_count":   doc.RefundedCount,
			"expired_count":    doc.ExpiredCount,
			"last_activity_at": doc.LastActivityAt,
		},
		"recall_source":       int(0),
		"recall_source_names": []string{serviceRecallSourceName},
		"normalized_score":    normalizedScore,
	}
	if len(reasons) > 0 {
		feat["rerank_reasons"] = reasons
	}
	out, err := json.Marshal(feat)
	if err != nil {
		return ""
	}
	return string(out)
}

// recallServices fetches up to recallSize active services matching the user's
// profile keywords and domain affinity. Returns merged stats so the
// serviceranker can score the success/latency signals.
func recallServices(ctx context.Context, keywords, domains []string, recallSize int) ([]sortDal.ServiceDoc, error) {
	if recallSize <= 0 {
		recallSize = 50
	}
	esReq := &sortDal.SearchServicesRequest{
		Query:   strings.Join(keywords, " "),
		Domains: domains,
		Limit:   recallSize,
	}
	esResp, err := sortDal.SearchServices(ctx, esReq)
	if err != nil {
		return nil, err
	}
	if len(esResp.Services) == 0 {
		return nil, nil
	}

	ids := make([]int64, 0, len(esResp.Services))
	for _, d := range esResp.Services {
		ids = append(ids, d.ServiceID)
	}
	stats, err := tradeDal.BatchGetServiceStats(db.DB, ids)
	if err != nil {
		logger.Ctx(ctx).Warn("service mix: stats fetch failed, ranking with zero stats", "err", err)
	} else {
		for i := range esResp.Services {
			st := stats[esResp.Services[i].ServiceID]
			esResp.Services[i].SuccessRate = st.SuccessRate
			esResp.Services[i].AvgLatencyMs = st.AvgLatencyMs
			esResp.Services[i].OrderCount = st.OrderCount
			esResp.Services[i].ReleasedCount = st.ReleasedCount
			esResp.Services[i].RefundedCount = st.RefundedCount
			esResp.Services[i].ExpiredCount = st.ExpiredCount
			esResp.Services[i].LastActivityAt = st.LastActivityAt
		}
	}
	return esResp.Services, nil
}

func countByType(cands []rank.Candidate, t rank.CandidateType) int {
	n := 0
	for _, c := range cands {
		if c.Type() == t {
			n++
		}
	}
	return n
}
