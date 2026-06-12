package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	"eigenflux_server/kitex_gen/eigenflux/base"
	"eigenflux_server/kitex_gen/eigenflux/sort"
	"eigenflux_server/pipeline/llm"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/json"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/metrics"
	"eigenflux_server/pkg/replaylog"
	"eigenflux_server/rpc/sort/dal"
	"eigenflux_server/rpc/sort/rank"
	"eigenflux_server/rpc/sort/rerank"
	"eigenflux_server/rpc/sort/serviceranker"
	tradeDal "eigenflux_server/rpc/trade/dal"
)

const (
	sourceAgent                = "agent"
	sourceLLMFallback          = "llm_fallback"
	sourceSingleIntentFallback = "single_intent_fallback"

	maxSubIntents            = 8
	maxIntentNameLen         = 64
	maxIntentQueryTextLen    = 256
	coverageFloorPerIntent   = 1
	coverageImportanceThresh = 0.5
	boundsCeilingPerService  = 10
	defaultMatchLimit        = 30
	maxMatchLimit            = 100
)

// SearchServices is the open-ended task→services discovery RPC. The handler:
//  1. Resolves sub-intents (agent-supplied → LLM decomposition → raw fallback)
//  2. Embeds each sub-intent's query text in parallel
//  3. Fans out kNN + BM25 recall per intent via dal.SearchServicesByIntents
//  4. Joins trading_service_stats
//  5. Scores each match per intent with the 6-signal ServiceRanker
//  6. Mixes per-intent results through dedup → normalize → coverage → bounds
//     rerank policies
func (s *SortServiceESImpl) SearchServices(ctx context.Context, req *sort.SearchServicesReq) (*sort.SearchServicesResp, error) {
	if req == nil || req.RawQuery == "" {
		return errResp("raw_query required"), nil
	}
	start := time.Now()
	defer func() { metrics.SearchServicesLatencyMs.WithLabelValues("total").Observe(elapsedMs(start)) }()
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = defaultMatchLimit
	} else if limit > maxMatchLimit {
		limit = maxMatchLimit
	}

	resolveStart := time.Now()
	effective, source := resolveSubIntents(ctx, req, chatClient)
	metrics.SearchServicesLatencyMs.WithLabelValues("resolve").Observe(elapsedMs(resolveStart))
	metrics.SearchServicesRequestsTotal.WithLabelValues(source).Inc()
	metrics.SearchServicesSubIntents.Observe(float64(len(effective)))
	if len(effective) == 0 {
		return errResp("no sub-intents produced"), nil
	}

	intentByName := make(map[string]llm.SubIntent, len(effective))
	importance := make(map[string]float64, len(effective))
	for _, si := range effective {
		intentByName[si.Name] = si
		importance[si.Name] = importanceOrDefaultF(si.Importance)
	}

	embedStart := time.Now()
	lanes, embedByName := buildIntentRecalls(ctx, effective, buildFilters(req))
	metrics.SearchServicesLatencyMs.WithLabelValues("embed").Observe(elapsedMs(embedStart))

	recallStart := time.Now()
	matches, err := dal.SearchServicesByIntents(ctx, lanes, perIntentRecallCap())
	metrics.SearchServicesLatencyMs.WithLabelValues("recall").Observe(elapsedMs(recallStart))
	if err != nil {
		return errResp("recall: " + err.Error()), nil
	}
	if len(matches) == 0 {
		metrics.SearchServicesEmptyTotal.Inc()
		return &sort.SearchServicesResp{
			Results:  nil,
			Debug:    buildDebug(source, effective),
			BaseResp: &base.BaseResp{Code: 0, Msg: "success"},
		}, nil
	}

	joinStats(matches)

	candidates, winningRanked := scorePerIntentAndWrap(matches, intentByName, importance, embedByName)
	if len(candidates) == 0 {
		return &sort.SearchServicesResp{
			Results:  nil,
			Debug:    buildDebug(source, effective),
			BaseResp: &base.BaseResp{Code: 0, Msg: "success"},
		}, nil
	}

	rerankStart := time.Now()
	mixer := rerank.New(
		&rerank.DedupPolicy{},
		&rerank.NormalizePolicy{Method: rerank.MinMax},
		&rerank.CoveragePolicy{
			Limit:               limit,
			FloorPerIntent:      coverageFloorPerIntent,
			ImportanceThreshold: coverageImportanceThresh,
			Importance:          importance,
		},
		&rerank.BoundsPolicy{
			Limit: limit,
			Bounds: map[rank.CandidateType]rerank.Bound{
				rank.CandidateService: {Ceiling: boundsCeilingPerService},
			},
		},
	)
	final := mixer.Rerank(candidates, limit)
	metrics.SearchServicesLatencyMs.WithLabelValues("rerank").Observe(elapsedMs(rerankStart))

	results := make([]*sort.SearchedService, 0, len(final))
	servedItems := make([]replaylog.ServedItem, 0, len(final))
	for i, c := range final {
		bc, ok := c.(*rank.BasicCandidate)
		if !ok {
			continue
		}
		rs := winningRanked[bc.ID()]
		if rs == nil {
			continue
		}
		results = append(results, buildMatchedResult(bc, rs, matches))
		servedItems = append(servedItems, replaylog.ServedItem{
			ItemID:       bc.ID(),
			ItemFeatures: buildSearchItemFeatures(bc, rs, matches),
			Score:        bc.Score(),
			Position:     i,
		})
	}

	publishSearchReplay(ctx, req, source, effective, servedItems)

	return &sort.SearchServicesResp{
		Results:  results,
		Debug:    buildDebug(source, effective),
		BaseResp: &base.BaseResp{Code: 0, Msg: "success"},
	}, nil
}

func elapsedMs(start time.Time) float64 {
	return float64(time.Since(start).Microseconds()) / 1000.0
}

func errResp(msg string) *sort.SearchServicesResp {
	return &sort.SearchServicesResp{
		Debug:    &sort.SearchServicesDebug{SubIntentsSource: "error", EffectiveSubIntents: []*sort.SubIntent{}},
		BaseResp: &base.BaseResp{Code: 400, Msg: msg},
	}
}

// resolveSubIntents picks the effective sub-intents for this request.
// source is one of sourceAgent / sourceLLMFallback / sourceSingleIntentFallback.
func resolveSubIntents(ctx context.Context, req *sort.SearchServicesReq, chat llm.Chat) ([]llm.SubIntent, string) {
	if len(req.SubIntents) > 0 {
		return trimAndCapAgentIntents(req.SubIntents), sourceAgent
	}
	if chat == nil {
		return singleIntent(req.RawQuery), sourceSingleIntentFallback
	}
	parsed, err := llm.DecomposeTask(ctx, chat, req.RawQuery)
	if err != nil {
		logger.Default().Warn("SearchServices decompose failed", "err", err)
		metrics.SearchServicesLLMFallbackTotal.WithLabelValues("llm_error").Inc()
		return singleIntent(req.RawQuery), sourceSingleIntentFallback
	}
	metrics.SearchServicesLLMFallbackTotal.WithLabelValues("agent_omitted").Inc()
	return capAndTrim(parsed), sourceLLMFallback
}

func trimAndCapAgentIntents(in []*sort.SubIntent) []llm.SubIntent {
	out := make([]llm.SubIntent, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, si := range in {
		if si == nil {
			continue
		}
		name := truncate(si.GetName(), maxIntentNameLen)
		if name == "" {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, llm.SubIntent{
			Name:       name,
			QueryText:  truncate(si.GetQueryText(), maxIntentQueryTextLen),
			Importance: importanceOrDefaultP(si.Importance),
		})
		if len(out) >= maxSubIntents {
			break
		}
	}
	return out
}

func capAndTrim(in []llm.SubIntent) []llm.SubIntent {
	if len(in) > maxSubIntents {
		in = in[:maxSubIntents]
	}
	for i := range in {
		in[i].Name = truncate(in[i].Name, maxIntentNameLen)
		in[i].QueryText = truncate(in[i].QueryText, maxIntentQueryTextLen)
		if in[i].Importance == 0 {
			in[i].Importance = 1.0
		}
	}
	return in
}

func singleIntent(raw string) []llm.SubIntent {
	return []llm.SubIntent{{Name: "raw", QueryText: raw, Importance: 1.0}}
}

// importanceOrDefaultP returns 1.0 when the pointer is nil (agent omitted the
// importance field).
func importanceOrDefaultP(p *float64) float64 {
	if p == nil {
		return 1.0
	}
	return *p
}

// importanceOrDefaultF returns 1.0 when the float value is zero, treating zero
// as "unset" since importance is a [0,1] weight where 0 makes the intent inert.
func importanceOrDefaultF(v float64) float64 {
	if v == 0 {
		return 1.0
	}
	return v
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func buildFilters(req *sort.SearchServicesReq) *dal.SearchServicesFilters {
	if req.Filters == nil {
		return nil
	}
	f := &dal.SearchServicesFilters{}
	if req.Filters.MaxPriceAtomic != nil {
		f.MaxPriceAtomic = *req.Filters.MaxPriceAtomic
	}
	if req.Filters.DeadlineMsMax != nil {
		f.DeadlineMsMax = *req.Filters.DeadlineMsMax
	}
	return f
}

// buildIntentRecalls embeds each sub-intent's query in parallel and returns
// the IntentRecall lanes plus a name->embedding map for downstream scoring.
// Failed embeddings are logged and produce BM25-only lanes (Embedding nil).
func buildIntentRecalls(ctx context.Context, intents []llm.SubIntent, filters *dal.SearchServicesFilters) ([]dal.IntentRecall, map[string][]float32) {
	out := make([]dal.IntentRecall, len(intents))
	embedByName := make(map[string][]float32, len(intents))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i, si := range intents {
		i, si := i, si
		wg.Add(1)
		go func() {
			defer wg.Done()
			var emb []float32
			if embeddingClient != nil {
				e, err := embeddingClient.GetEmbedding(ctx, si.QueryText)
				if err != nil {
					logger.Default().Warn("SearchServices sub-intent embedding failed", "intent", si.Name, "err", err)
				} else {
					emb = e
				}
			}
			mu.Lock()
			out[i] = dal.IntentRecall{
				Name:      si.Name,
				QueryText: si.QueryText,
				Embedding: emb,
				Filters:   filters,
			}
			embedByName[si.Name] = emb
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out, embedByName
}

func perIntentRecallCap() int {
	if v := os.Getenv("TASK_MATCH_RECALL_SIZE_PER_INTENT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 50
}

// joinStats batch-fetches trading_service_stats from the trade DAL and
// merges the rolling counters (success_rate, avg_latency_ms, order/release/
// refund/expire counts, last_activity_at) into each IntentMatch in place.
// Stats fetch failure is logged and matches are left with zero stats —
// downstream ranking degrades gracefully.
func joinStats(matches []dal.IntentMatch) {
	if len(matches) == 0 {
		return
	}
	ids := make([]int64, 0, len(matches))
	for _, m := range matches {
		ids = append(ids, m.ServiceID)
	}
	stats, err := tradeDal.BatchGetServiceStats(db.DB, ids)
	if err != nil {
		logger.Default().Warn("SearchServices stats fetch failed", "err", err)
		return
	}
	for i := range matches {
		st := stats[matches[i].ServiceID]
		matches[i].SuccessRate = st.SuccessRate
		matches[i].AvgLatencyMs = st.AvgLatencyMs
		matches[i].OrderCount = st.OrderCount
		matches[i].ReleasedCount = st.ReleasedCount
		matches[i].RefundedCount = st.RefundedCount
		matches[i].ExpiredCount = st.ExpiredCount
		matches[i].LastActivityAt = st.LastActivityAt
	}
}

// scorePerIntentAndWrap scores each match against each intent that recalled
// it, records the per-intent score map, picks the winning intent as
// argmax(score * importance), and wraps the result as *rank.BasicCandidate.
// Returns the candidates and a serviceID -> winning RankedService map used
// downstream to build the response score_breakdown.
func scorePerIntentAndWrap(
	matches []dal.IntentMatch,
	intentByName map[string]llm.SubIntent,
	importance map[string]float64,
	embedByName map[string][]float32,
) ([]rank.Candidate, map[int64]*serviceranker.RankedService) {
	svcRanker := serviceranker.New(serviceRankerCfg)
	candidates := make([]rank.Candidate, 0, len(matches))
	winning := make(map[int64]*serviceranker.RankedService, len(matches))

	for i := range matches {
		m := &matches[i]
		perIntent := make(map[string]float64, len(m.MatchedIntents))
		var bestRanked *serviceranker.RankedService
		var bestIntent string
		bestAggregate := -1.0
		for _, name := range m.MatchedIntents {
			if _, ok := intentByName[name]; !ok {
				continue
			}
			ranked := svcRanker.Rank([]dal.ServiceDoc{m.ServiceDoc}, embedByName[name], 1)
			if len(ranked) == 0 {
				continue
			}
			perIntent[name] = ranked[0].Score
			aggregate := ranked[0].Score * importance[name]
			if aggregate > bestAggregate {
				bestAggregate = aggregate
				rs := ranked[0]
				bestRanked = &rs
				bestIntent = name
			}
		}
		if bestRanked == nil {
			continue
		}
		bc := rank.NewCandidate(m.ServiceID, rank.CandidateService, bestAggregate, nil, nil)
		bc.SetMatchedIntents(m.MatchedIntents)
		bc.SetPerIntentScore(perIntent)
		bc.SetWinningIntent(bestIntent)
		candidates = append(candidates, bc)
		winning[m.ServiceID] = bestRanked
	}
	return candidates, winning
}

func buildMatchedResult(bc *rank.BasicCandidate, rs *serviceranker.RankedService, matches []dal.IntentMatch) *sort.SearchedService {
	var doc dal.ServiceDoc
	for _, m := range matches {
		if m.ServiceID == bc.ID() {
			doc = m.ServiceDoc
			break
		}
	}
	return &sort.SearchedService{
		ServiceId:          doc.ServiceID,
		Title:              doc.Title,
		SellerAgentId:      doc.SellerAgentID,
		AmountAtomic:       doc.AmountAtomic,
		Asset:              doc.Asset,
		DeliveryDeadlineMs: doc.DeliveryDeadlineMs,
		Score:              bc.Score(),
		MatchedIntents:     bc.MatchedIntents(),
		WinningIntent:      bc.WinningIntent(),
		ScoreBreakdown: map[string]float64{
			"semantic": rs.Breakdown.Semantic,
			"keyword":  rs.Breakdown.Keyword,
			"success":  rs.Breakdown.Success,
			"latency":  rs.Breakdown.Latency,
			"price":    rs.Breakdown.Price,
			"deadline": rs.Breakdown.Deadline,
		},
		Stats: map[string]float64{
			"success_rate":   doc.SuccessRate,
			"avg_latency_ms": float64(doc.AvgLatencyMs),
			"order_count":    float64(doc.OrderCount),
			"released_count": float64(doc.ReleasedCount),
		},
	}
}

// buildSearchItemFeatures emits the JSON shape consumed by the replay log
// for one served service.
func buildSearchItemFeatures(bc *rank.BasicCandidate, rs *serviceranker.RankedService, matches []dal.IntentMatch) string {
	var doc dal.ServiceDoc
	for _, m := range matches {
		if m.ServiceID == bc.ID() {
			doc = m.ServiceDoc
			break
		}
	}
	payload := map[string]interface{}{
		"entry_type":       "service",
		"service_id":       bc.ID(),
		"seller_agent_id":  doc.SellerAgentID,
		"title":            doc.Title,
		"matched_intents":  bc.MatchedIntents(),
		"per_intent_score": bc.PerIntentScore(),
		"winning_intent":   bc.WinningIntent(),
		"normalized_score": bc.Score(),
		"rerank_reasons":   bc.Reasons(),
		"rank_scores": map[string]float64{
			"semantic": rs.Breakdown.Semantic,
			"keyword":  rs.Breakdown.Keyword,
			"success":  rs.Breakdown.Success,
			"latency":  rs.Breakdown.Latency,
			"price":    rs.Breakdown.Price,
			"deadline": rs.Breakdown.Deadline,
		},
		"stats": map[string]float64{
			"success_rate":   doc.SuccessRate,
			"avg_latency_ms": float64(doc.AvgLatencyMs),
			"order_count":    float64(doc.OrderCount),
			"released_count": float64(doc.ReleasedCount),
			"refunded_count": float64(doc.RefundedCount),
			"expired_count":  float64(doc.ExpiredCount),
		},
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return string(b)
}

// publishSearchReplay emits the request-scope agent_features envelope and
// the per-served-item features to stream:replay:log so offline replay can
// re-evaluate SearchServices traffic. Failures are logged and swallowed
// — replay is best-effort.
func publishSearchReplay(ctx context.Context, req *sort.SearchServicesReq, source string, effective []llm.SubIntent, served []replaylog.ServedItem) {
	if len(served) == 0 {
		return
	}
	agentFeatures := map[string]interface{}{
		"surface":             "search_services",
		"raw_query":           req.RawQuery,
		"sub_intents_source":  source,
		"effective_sub_intents": effective,
	}
	if req.Filters != nil {
		f := map[string]interface{}{}
		if req.Filters.MaxPriceAtomic != nil {
			f["max_price_atomic"] = *req.Filters.MaxPriceAtomic
		}
		if req.Filters.DeadlineMsMax != nil {
			f["deadline_ms_max"] = *req.Filters.DeadlineMsMax
		}
		agentFeatures["filters"] = f
	}
	agentFeaturesJSON, err := json.Marshal(agentFeatures)
	if err != nil {
		logger.Default().Warn("SearchServices replay agent_features marshal failed", "err", err)
		return
	}
	impressionID := fmt.Sprintf("imp_search_%d", time.Now().UnixNano())
	if err := replaylog.Publish(ctx, impressionID, 0, string(agentFeaturesJSON), served); err != nil {
		logger.Default().Warn("SearchServices replay publish failed", "err", err)
	}
}

func buildDebug(source string, effective []llm.SubIntent) *sort.SearchServicesDebug {
	out := &sort.SearchServicesDebug{
		SubIntentsSource:    source,
		EffectiveSubIntents: make([]*sort.SubIntent, 0, len(effective)),
	}
	for i := range effective {
		imp := effective[i].Importance
		out.EffectiveSubIntents = append(out.EffectiveSubIntents, &sort.SubIntent{
			Name:       effective[i].Name,
			QueryText:  effective[i].QueryText,
			Importance: &imp,
		})
	}
	return out
}
