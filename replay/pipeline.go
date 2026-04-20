package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"eigenflux_server/pkg/db"
	embcodec "eigenflux_server/pkg/embedding"
	"eigenflux_server/pkg/logger"
	profileDal "eigenflux_server/rpc/profile/dal"
	sortDal "eigenflux_server/rpc/sort/dal"
	"eigenflux_server/rpc/sort/ranker"
)

type pipelineParams struct {
	agentID           int64
	agentProfile      *ReplayAgentProfile
	now               time.Time
	useFeedHistory    bool
	limit             int
	rankerCfg         *ranker.RankerConfig
	keywordRecallSize int
	enableKNN         bool
	knnK              int
	knnCandidates     int
}

func runReplayPipeline(ctx context.Context, p *pipelineParams) (*ReplayResponse, error) {
	keywords, domains, geo, profileEmbedding, err := resolveProfile(ctx, p.agentID, p.agentProfile)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve profile: %w", err)
	}

	profileSummary := ReplayProfileSummary{
		Keywords:     keywords,
		Domains:      domains,
		Geo:          geo,
		HasEmbedding: len(profileEmbedding) > 0,
	}

	var keywordItems []sortDal.Item
	var knnItems []sortDal.Item
	var keywordErr, knnErr error
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		searchReq := &sortDal.SearchItemsRequest{
			Limit:           p.keywordRecallSize,
			Domains:         domains,
			Keywords:        keywords,
			Geo:             geo,
			FreshnessOffset: cfg.FreshnessOffset,
			FreshnessScale:  cfg.FreshnessScale,
			FreshnessDecay:  cfg.FreshnessDecay,
			Now:             p.now,
		}
		resp, err := sortDal.SearchItems(ctx, searchReq)
		if err != nil {
			keywordErr = err
			return
		}
		keywordItems = resp.Items
	}()

	if p.enableKNN && len(profileEmbedding) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			filters := sortDal.BuildRecallFilters("", p.now)
			knnItems, knnErr = sortDal.SearchByEmbedding(ctx, profileEmbedding, filters, p.knnK, p.knnCandidates)
			if knnErr != nil {
				logger.Ctx(ctx).Warn("kNN recall failed", "err", knnErr)
			}
		}()
	}

	wg.Wait()
	if keywordErr != nil {
		return nil, fmt.Errorf("keyword recall failed: %w", keywordErr)
	}

	stats := ReplayStats{
		KeywordRecallCount: len(keywordItems),
		KNNRecallCount:     len(knnItems),
	}

	esItems := keywordItems
	seen := make(map[int64]bool, len(esItems))
	for _, item := range esItems {
		seen[item.ID] = true
	}
	if knnErr == nil {
		for _, item := range knnItems {
			if !seen[item.ID] {
				esItems = append(esItems, item)
				seen[item.ID] = true
			}
		}
	}
	stats.MergedCount = len(esItems)

	filtered := make([]sortDal.Item, 0, len(esItems))
	for _, item := range esItems {
		if !item.UpdatedAt.After(p.now) {
			filtered = append(filtered, item)
		}
	}
	esItems = filtered

	esItemMap := make(map[int64]sortDal.Item, len(esItems))
	for _, item := range esItems {
		esItemMap[item.ID] = item
	}

	r := ranker.New(p.rankerCfg)
	userProfile := &ranker.UserProfile{
		Keywords:  keywords,
		Domains:   domains,
		Geo:       geo,
		Embedding: profileEmbedding,
	}
	allRanked := r.RankAt(esItems, userProfile, len(esItems), p.now)

	allRanked, _ = collapseRankedByGroup(allRanked, esItemMap)
	stats.AfterGroupDedupCount = len(allRanked)

	ranked := make([]ranker.RankedItem, 0, len(allRanked))
	belowThreshold := make([]ranker.RankedItem, 0)
	for _, ri := range allRanked {
		if ri.Score >= p.rankerCfg.MinRelevanceScore {
			ranked = append(ranked, ri)
		} else {
			belowThreshold = append(belowThreshold, ri)
		}
	}
	stats.AboveThresholdCount = len(ranked)

	var explorationRanked []ranker.RankedItem
	if p.rankerCfg.ExplorationSlots > 0 {
		rankedIDs := make(map[int64]bool, len(ranked))
		rankedGroupIDs := make(map[int64]bool, len(ranked))
		for _, ri := range ranked {
			rankedIDs[ri.ItemID] = true
			if item, ok := esItemMap[ri.ItemID]; ok && item.GroupID != 0 {
				rankedGroupIDs[item.GroupID] = true
			}
		}
		explorationItems := ranker.PickExplorationItemsAt(esItems, rankedIDs, rankedGroupIDs, p.rankerCfg.ExplorationSlots, 48*time.Hour, 0.5, p.now)
		for _, ei := range explorationItems {
			explorationRanked = append(explorationRanked, ranker.RankedItem{ItemID: ei.ID, Score: 0.0})
		}
	}

	bloomFiltered := 0
	seenGroupIDs := make(map[int64]bool)
	if p.useFeedHistory && bf != nil {
		allGroupIDs := make([]int64, 0)
		for _, ri := range ranked {
			if item, ok := esItemMap[ri.ItemID]; ok && item.GroupID != 0 {
				allGroupIDs = append(allGroupIDs, item.GroupID)
			}
		}
		if len(allGroupIDs) > 0 {
			seenMap, err := bf.CheckExists(ctx, p.agentID, allGroupIDs)
			if err != nil {
				logger.Ctx(ctx).Warn("bloom filter check failed", "err", err)
			} else {
				seenGroupIDs = seenMap
			}
		}
	}

	rankedResponse := make([]ReplayItem, 0, p.limit)
	pos := 0
	for _, ri := range ranked {
		item, ok := esItemMap[ri.ItemID]
		if !ok {
			continue
		}
		if item.GroupID != 0 && seenGroupIDs[item.GroupID] {
			bloomFiltered++
			continue
		}
		rankedResponse = append(rankedResponse, buildReplayItem(item, ri, pos))
		pos++
		if len(rankedResponse) >= p.limit {
			break
		}
	}
	stats.BloomFilteredCount = bloomFiltered

	filteredResponse := make([]ReplayItem, 0, len(belowThreshold))
	for i, ri := range belowThreshold {
		if item, ok := esItemMap[ri.ItemID]; ok {
			filteredResponse = append(filteredResponse, buildReplayItem(item, ri, i))
		}
	}

	explorationResponse := make([]ReplayItem, 0, len(explorationRanked))
	for i, ri := range explorationRanked {
		if item, ok := esItemMap[ri.ItemID]; ok {
			explorationResponse = append(explorationResponse, buildReplayItem(item, ri, i))
		}
	}

	return &ReplayResponse{
		RankedItems:      rankedResponse,
		FilteredItems:    filteredResponse,
		ExplorationItems: explorationResponse,
		AgentProfile:     profileSummary,
		ConfigUsed:       buildConfigSummary(p),
		Stats:            stats,
	}, nil
}

func resolveProfile(ctx context.Context, agentID int64, override *ReplayAgentProfile) (keywords, domains []string, geo string, embedding []float32, err error) {
	if override != nil && len(override.Keywords) > 0 && len(override.Domains) > 0 {
		keywords = override.Keywords
		domains = override.Domains
		geo = override.Geo
		embedding = override.Embedding
		return
	}

	ap, dbErr := profileDal.GetAgentProfile(db.DB, agentID)
	if dbErr != nil {
		if override != nil {
			keywords = override.Keywords
			domains = override.Domains
			geo = override.Geo
			embedding = override.Embedding
			return
		}
		err = fmt.Errorf("agent %d profile not found: %w", agentID, dbErr)
		return
	}

	if ap.Keywords != "" && ap.Status == 3 {
		kws := strings.Split(ap.Keywords, ",")
		for _, kw := range kws {
			kw = strings.TrimSpace(kw)
			if kw != "" {
				keywords = append(keywords, kw)
			}
		}
		domains = keywords
	}
	if len(ap.ProfileEmbedding) > 0 {
		embedding = embcodec.Decode(ap.ProfileEmbedding)
	}

	if override != nil {
		if len(override.Keywords) > 0 {
			keywords = override.Keywords
		}
		if len(override.Domains) > 0 {
			domains = override.Domains
		}
		if override.Geo != "" {
			geo = override.Geo
		}
		if len(override.Embedding) > 0 {
			embedding = override.Embedding
		}
	}
	return
}

func collapseRankedByGroup(ranked []ranker.RankedItem, itemMap map[int64]sortDal.Item) ([]ranker.RankedItem, int) {
	if len(ranked) == 0 {
		return nil, 0
	}
	collapsed := make([]ranker.RankedItem, 0, len(ranked))
	seenGroupIDs := make(map[int64]bool, len(ranked))
	seenItemIDs := make(map[int64]bool, len(ranked))
	filteredCount := 0
	for _, ri := range ranked {
		if seenItemIDs[ri.ItemID] {
			filteredCount++
			continue
		}
		item, ok := itemMap[ri.ItemID]
		if !ok {
			collapsed = append(collapsed, ri)
			seenItemIDs[ri.ItemID] = true
			continue
		}
		if item.GroupID != 0 {
			if seenGroupIDs[item.GroupID] {
				filteredCount++
				continue
			}
			seenGroupIDs[item.GroupID] = true
		}
		seenItemIDs[ri.ItemID] = true
		collapsed = append(collapsed, ri)
	}
	return collapsed, filteredCount
}

func buildReplayItem(item sortDal.Item, ri ranker.RankedItem, position int) ReplayItem {
	itemData := map[string]any{
		"content":        item.Content,
		"summary":        item.Summary,
		"broadcast_type": item.Type,
		"keywords":       item.Keywords,
		"domains":        item.Domains,
		"geo":            item.Geo,
		"source_type":    item.SourceType,
		"quality_score":  item.QualityScore,
		"group_id":       item.GroupID,
		"lang":           item.Lang,
		"timeliness":     item.Timeliness,
		"updated_at":     item.UpdatedAt.Format(time.RFC3339),
		"created_at":     item.CreatedAt.Format(time.RFC3339),
	}
	if item.ExpireTime != nil {
		itemData["expire_time"] = item.ExpireTime.Format(time.RFC3339)
	}
	return ReplayItem{
		ItemID:   item.ID,
		Position: position,
		Score:    ri.Score,
		Scores: map[string]any{
			"semantic":  ri.Scores.Semantic,
			"keyword":   ri.Scores.Keyword,
			"freshness": ri.Scores.Freshness,
			"total":     ri.Scores.Total,
			"is_draft":  ri.Scores.IsDraft,
		},
		Item: itemData,
	}
}

func buildConfigSummary(p *pipelineParams) ReplayConfigSummary {
	return ReplayConfigSummary{
		RankerParams: map[string]any{
			"alpha":               p.rankerCfg.Alpha,
			"beta":                p.rankerCfg.Beta,
			"gamma":               p.rankerCfg.Gamma,
			"delta":               p.rankerCfg.Delta,
			"min_relevance_score": p.rankerCfg.MinRelevanceScore,
			"urgency_boost":       p.rankerCfg.UrgencyBoost,
			"urgency_window":      p.rankerCfg.UrgencyWindow.String(),
			"exploration_slots":   p.rankerCfg.ExplorationSlots,
			"draft_dampening":     p.rankerCfg.DraftDampening,
			"freshness":           p.rankerCfg.Freshness,
		},
		RecallParams: map[string]any{
			"keyword_recall_size":   p.keywordRecallSize,
			"enable_knn_recall":     p.enableKNN,
			"knn_recall_k":          p.knnK,
			"knn_recall_candidates": p.knnCandidates,
		},
	}
}
