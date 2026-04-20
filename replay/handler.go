package main

import (
	"context"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"

	"eigenflux_server/pkg/json"
	"eigenflux_server/pkg/logger"
)

type ReplayRequest struct {
	AgentID        int64               `json:"agent_id"`
	AgentProfile   *ReplayAgentProfile `json:"agent_profile,omitempty"`
	SimulatedAt    *string             `json:"simulated_at,omitempty"`
	UseFeedHistory bool                `json:"use_feed_history"`
	Limit          int                 `json:"limit,omitempty"`
	RankerParams   *ReplayRankerParams `json:"ranker_params,omitempty"`
	RecallParams   *ReplayRecallParams `json:"recall_params,omitempty"`
}

type ReplayAgentProfile struct {
	Keywords  []string  `json:"keywords,omitempty"`
	Domains   []string  `json:"domains,omitempty"`
	Geo       string    `json:"geo,omitempty"`
	Embedding []float32 `json:"embedding,omitempty"`
}

type ReplayResponse struct {
	RankedItems      []ReplayItem         `json:"ranked_items"`
	FilteredItems    []ReplayItem         `json:"filtered_items"`
	ExplorationItems []ReplayItem         `json:"exploration_items"`
	AgentProfile     ReplayProfileSummary `json:"agent_profile"`
	ConfigUsed       ReplayConfigSummary  `json:"config_used"`
	Stats            ReplayStats          `json:"stats"`
}

type ReplayItem struct {
	ItemID   int64          `json:"item_id"`
	Position int            `json:"position"`
	Score    float64        `json:"score"`
	Scores   map[string]any `json:"scores"`
	Item     map[string]any `json:"item"`
}

type ReplayProfileSummary struct {
	Keywords     []string `json:"keywords"`
	Domains      []string `json:"domains"`
	Geo          string   `json:"geo"`
	HasEmbedding bool     `json:"has_embedding"`
}

type ReplayConfigSummary struct {
	RankerParams map[string]any `json:"ranker_params"`
	RecallParams map[string]any `json:"recall_params"`
}

type ReplayStats struct {
	KeywordRecallCount   int   `json:"keyword_recall_count"`
	KNNRecallCount       int   `json:"knn_recall_count"`
	MergedCount          int   `json:"merged_count"`
	AfterGroupDedupCount int   `json:"after_group_dedup_count"`
	AboveThresholdCount  int   `json:"above_threshold_count"`
	BloomFilteredCount   int   `json:"bloom_filtered_count"`
	TotalLatencyMs       int64 `json:"total_latency_ms"`
}

func handleReplaySort(ctx context.Context, c *app.RequestContext) {
	start := time.Now()

	var req ReplayRequest
	if err := c.BindJSON(&req); err != nil {
		c.JSON(consts.StatusBadRequest, map[string]string{"error": "invalid request body: " + err.Error()})
		return
	}

	if req.AgentID <= 0 {
		c.JSON(consts.StatusBadRequest, map[string]string{"error": "agent_id is required"})
		return
	}

	limit := req.Limit
	if limit <= 0 {
		limit = 20
	}

	now := time.Now()
	if req.SimulatedAt != nil && *req.SimulatedAt != "" {
		parsed, err := time.Parse(time.RFC3339, *req.SimulatedAt)
		if err != nil {
			c.JSON(consts.StatusBadRequest, map[string]string{"error": "invalid simulated_at format, use RFC3339: " + err.Error()})
			return
		}
		now = parsed
	}

	rankerCfg := mergeRankerConfig(baseCfg, req.RankerParams)
	keywordRecallSize, enableKNN, knnK, knnCandidates := mergeRecallParams(cfg, req.RecallParams)

	logger.Ctx(ctx).Info("replay request",
		"agentID", req.AgentID,
		"simulatedAt", now,
		"useFeedHistory", req.UseFeedHistory,
		"limit", limit,
	)

	result, err := runReplayPipeline(ctx, &pipelineParams{
		agentID:           req.AgentID,
		agentProfile:      req.AgentProfile,
		now:               now,
		useFeedHistory:    req.UseFeedHistory,
		limit:             limit,
		rankerCfg:         rankerCfg,
		keywordRecallSize: keywordRecallSize,
		enableKNN:         enableKNN,
		knnK:              knnK,
		knnCandidates:     knnCandidates,
	})
	if err != nil {
		logger.Ctx(ctx).Error("replay pipeline failed", "err", err)
		c.JSON(consts.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	result.Stats.TotalLatencyMs = time.Since(start).Milliseconds()

	body, _ := json.Marshal(result)
	c.Data(consts.StatusOK, "application/json", body)
}
