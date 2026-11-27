package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"eigenflux_server/pkg/metrics"
	"eigenflux_server/rpc/sort/discoverylr"
)

const feedbackExperiment = "discovery-feedback-dcnv2-v1"
const feedbackSalt = feedbackExperiment + ":"
const feedbackContract = "feedback_history_agent_item_v1"

type FeedbackEvidence struct {
	ModelVersion   string              `json:"model_version"`
	Contract       string              `json:"feature_contract_version"`
	SnapshotCutoff int64               `json:"snapshot_cutoff_ms"`
	Score          float64             `json:"score"`
	ItemCold       bool                `json:"item_cold"`
	Baseline       *discoverylr.Result `json:"baseline_lr"`
}

type feedbackInput struct {
	Item     int64     `json:"item_id,string"`
	Kind     string    `json:"content_type"`
	Channels []string  `json:"channels"`
	Features []float64 `json:"rating_features"`
}

type feedbackBatch struct {
	Owner      int64           `json:"agent_id,string"`
	When       int64           `json:"request_time"`
	Candidates []feedbackInput `json:"candidates"`
}

type feedbackResponse struct {
	Version  string `json:"model_version"`
	Contract string `json:"feature_contract_version"`
	Cutoff   int64  `json:"snapshot_cutoff_ms"`
	Rows     []struct {
		Score *float64 `json:"score"`
		Cold  *bool    `json:"item_cold"`
	} `json:"rows"`
}

type FeedbackClient struct {
	url    string
	client *http.Client
}

func NewFeedbackClient() *FeedbackClient {
	return &FeedbackClient{"http://127.0.0.1:8771/score", &http.Client{Timeout: 100 * time.Millisecond}}
}

func (f *FeedbackClient) score(ctx context.Context, batch feedbackBatch) (feedbackResponse, bool) {
	var result feedbackResponse
	data, err := json.Marshal(batch)
	if err != nil {
		return result, false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.url, bytes.NewReader(data))
	if err != nil {
		return result, false
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.client.Do(req)
	if err != nil {
		return result, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return result, false
	}
	data, err = io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil || json.Unmarshal(data, &result) != nil {
		return result, false
	}
	const dayMS int64 = 86400000
	const offsetMS int64 = 8 * 3600000
	cutoff := (batch.When+offsetMS)/dayMS*dayMS - offsetMS
	if result.Version == "" || result.Contract != feedbackContract || result.Cutoff != cutoff || len(result.Rows) != len(batch.Candidates) {
		return result, false
	}
	for _, r := range result.Rows {
		if r.Score == nil || !finite(*r.Score) || *r.Score < -1 || *r.Score > 2 || r.Cold == nil {
			return result, false
		}
	}
	return result, true
}

func (e *Engine) applyFeedback(ctx context.Context, owner int64, candidates []Candidate, indices []int, channels map[int64][]string, state *ratingRequest) {
	if e.Feedback == nil || state.feedbackFailed || !bucketEnabled(feedbackSalt, owner, e.FeedbackBPS) {
		return
	}
	batch := feedbackBatch{Owner: owner, When: candidates[indices[0]].Score.RequestTime, Candidates: make([]feedbackInput, 0, len(indices))}
	for _, i := range indices {
		c := candidates[i]
		batch.Candidates = append(batch.Candidates, feedbackInput{
			Item: c.Document.Ref.ID, Kind: c.Document.ContentType,
			Channels: channels[c.Document.Ref.ID], Features: c.RatingFeatures[:len(discoverylr.RatingFeatureNames)],
		})
	}
	result, ok := e.Feedback.score(ctx, batch)
	if !ok || (state.feedbackVersion != "" && state.feedbackVersion != result.Version) {
		state.feedbackFailed = true
		metrics.DiscoveryLRScoring.WithLabelValues("feedback_dcn_fallback").Inc()
		return
	}
	state.feedbackVersion = result.Version
	for j, i := range indices {
		c := &candidates[i]
		row := result.Rows[j]
		c.Feedback = &FeedbackEvidence{
			ModelVersion: result.Version, Contract: result.Contract, SnapshotCutoff: result.Cutoff,
			Score: *row.Score, ItemCold: *row.Cold, Baseline: c.LR,
		}
		c.LR = nil
		c.FinalScore = c.RankingScore()
	}
	metrics.DiscoveryLRScoring.WithLabelValues("feedback_dcn_scored").Inc()
}

// Count the final selection once per assigned Sort request, including a refill.
// GBDT takes precedence over this experiment in both arms.
func (e *Engine) feedbackOutcome(arm string, owner int64, candidates []Candidate, err error) string {
	if err != nil {
		return "error"
	}
	if len(candidates) == 0 {
		return "empty"
	}
	c := candidates[0]
	if c.LR != nil && c.LR.Contract == discoverylr.SnapshotContract {
		return "gbdt_bypass"
	}
	if arm == "treatment" && c.Feedback != nil {
		return "served"
	}
	if arm == "control" && ratingRanked(c) && c.LR != nil {
		if bucketEnabled(ratingNextSalt, owner, e.RatingNextBPS) && c.LR.Contract != discoverylr.RatingV2Contract {
			return "fallback"
		}
		return "served"
	}
	return "fallback"
}
