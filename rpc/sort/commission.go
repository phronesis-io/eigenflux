package main

import (
	"context"
	"eigenflux_server/kitex_gen/eigenflux/base"
	"eigenflux_server/kitex_gen/eigenflux/sort"
	"eigenflux_server/pipeline/embedding"
	"eigenflux_server/pkg/commissionindex"
	"eigenflux_server/pkg/db"
	embcodec "eigenflux_server/pkg/embedding"
	"eigenflux_server/pkg/json"
	"eigenflux_server/pkg/metrics"
	profileDal "eigenflux_server/rpc/profile/dal"
	"fmt"
	"strings"
	"time"
)

func (s *SortServiceESImpl) SearchCommissions(ctx context.Context, req *sort.SearchCommissionsReq) (*sort.SearchCommissionsResp, error) {
	if strings.TrimSpace(req.GetQuery()) == "" {
		return commissionSearchError("query is required"), nil
	}
	embedder := embedding.NewClient(cfg.EmbeddingProvider, cfg.EmbeddingApiKey, cfg.EmbeddingBaseURL, cfg.EmbeddingModel, cfg.EmbeddingDimensions)
	vector, err := embedder.GetEmbedding(ctx, req.GetQuery())
	if err != nil {
		return commissionSearchError(fmt.Sprintf("embed Commission query: %v", err)), nil
	}
	candidates, err := searchCommissions(ctx, req.GetQuery(), vector, req.GetFilters(), int(req.GetLimit()))
	if err != nil {
		return commissionSearchError(err.Error()), nil
	}
	return &sort.SearchCommissionsResp{Candidates: candidates, BaseResp: &base.BaseResp{Code: 0, Msg: "success"}}, nil
}

func (s *SortServiceESImpl) RecommendCommissions(ctx context.Context, req *sort.RecommendCommissionsReq) (*sort.RecommendCommissionsResp, error) {
	if req.GetAgentId() <= 0 {
		return commissionRecommendError("agent_id is required"), nil
	}
	profile, err := profileDal.GetAgentProfile(db.DB, req.GetAgentId())
	if err != nil || profile == nil || profile.Status != 3 {
		return commissionRecommendError("completed profile not found"), nil
	}
	query := commissionindex.NormalizeText(profile.Keywords, profile.Country)
	if query == "" {
		return commissionRecommendError("profile has no discovery features"), nil
	}
	vector := embcodec.Decode(profile.ProfileEmbedding)
	candidates, err := searchCommissions(ctx, query, vector, req.GetFilters(), int(req.GetLimit()))
	if err != nil {
		return commissionRecommendError(err.Error()), nil
	}
	return &sort.RecommendCommissionsResp{Candidates: candidates, BaseResp: &base.BaseResp{Code: 0, Msg: "success"}}, nil
}

func searchCommissions(ctx context.Context, query string, vector []float32, filters *sort.CommissionSearchFilters, limit int) ([]*sort.CommissionCandidate, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	request := commissionindex.SearchRequest{Query: query, Embedding: vector, Limit: limit}
	if filters != nil {
		request.MinPriceFen = filters.GetMinPriceFen()
		request.MaxPriceFen = filters.GetMaxPriceFen()
		request.MinDurationMS = filters.GetMinPromisedDeliveryMs()
		request.MaxDurationMS = filters.GetMaxPromisedDeliveryMs()
	}
	start := time.Now()
	hits, err := commissionindex.ESStore{Index: cfg.CommissionIndexName, Alias: cfg.CommissionIndexAlias, Dimensions: cfg.EmbeddingDimensions}.Search(ctx, request)
	metrics.CommissionDiscoveryDuration.WithLabelValues("search").Observe(time.Since(start).Seconds())
	if err != nil {
		return nil, err
	}
	out := make([]*sort.CommissionCandidate, 0, len(hits))
	for _, hit := range hits {
		score := commissionScore(hit)
		features, _ := json.Marshal(map[string]any{"keyword_score": hit.KeywordScore, "semantic_score": hit.SemanticScore, "completion_rate_bps": hit.Document.CompletionRateBPS, "rating_milli": hit.Document.AverageRatingMilli, "completed_count": hit.Document.CompletedCount})
		featureString := string(features)
		out = append(out, &sort.CommissionCandidate{CommissionId: hit.Document.CommissionID, Score: score, Features: &featureString})
	}
	sortCommissionCandidates(out)
	return out, nil
}

func commissionScore(hit commissionindex.Hit) float64 {
	relevance := hit.KeywordScore
	if hit.SemanticScore > relevance {
		relevance = hit.SemanticScore
	}
	completion := float64(hit.Document.CompletionRateBPS) / 10000
	rating := float64(hit.Document.AverageRatingMilli) / 5000
	if !hit.Document.HasRating {
		rating = 0
	}
	evidence := float64(hit.Document.CompletedCount) / (float64(hit.Document.CompletedCount) + 10)
	return relevance + 0.20*completion + 0.15*rating + 0.05*evidence
}
func sortCommissionCandidates(values []*sort.CommissionCandidate) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && (values[j].Score > values[j-1].Score || (values[j].Score == values[j-1].Score && values[j].CommissionId < values[j-1].CommissionId)); j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
func commissionSearchError(message string) *sort.SearchCommissionsResp {
	return &sort.SearchCommissionsResp{Candidates: []*sort.CommissionCandidate{}, BaseResp: &base.BaseResp{Code: 400, Msg: message}}
}
func commissionRecommendError(message string) *sort.RecommendCommissionsResp {
	return &sort.RecommendCommissionsResp{Candidates: []*sort.CommissionCandidate{}, BaseResp: &base.BaseResp{Code: 400, Msg: message}}
}
