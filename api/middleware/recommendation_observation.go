package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"strconv"
	"time"

	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/metrics"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/google/uuid"
)

// Both arms use the same serving policy. This namespace must remain fixed for
// the complete observation window, including API restarts and client upgrades.
const recommendationAAExperiment = "recommendation_aa_v1"
const observationBodyLimit = 1 << 20

type recommendationObservation struct {
	ObservationID       string `json:"observation_id"`
	ExperimentID        string `json:"experiment_id"`
	Arm                 string `json:"arm"`
	AgentID             string `json:"agent_id"`
	Endpoint            string `json:"endpoint"`
	Caller              string `json:"caller"`
	StartedAt           int64  `json:"started_at"`
	CompletedAt         int64  `json:"completed_at"`
	HTTPStatus          int    `json:"http_status"`
	ErrorCode           string `json:"error_code"`
	Outcome             string `json:"outcome"`
	ItemCount           *int   `json:"item_count"`
	ImpressionID        string `json:"impression_id"`
	PipelineVersion     string `json:"pipeline_version"`
	ResultStatus        string `json:"result_status"`
	Partial             bool   `json:"partial"`
	ResponseInputOrigin string `json:"response_input_origin"`
	FallbackReason      string `json:"fallback_reason"`
}

type recommendationMetadata struct {
	PipelineVersion string `json:"pipeline_version"`
	ResultStatus    string `json:"result_status"`
	Partial         bool   `json:"partial"`
	InputOrigin     string `json:"input_origin"`
	FallbackReason  string `json:"fallback_reason"`
}

func recommendationAAArm(agentID int64) string {
	digest := sha256.Sum256([]byte(recommendationAAExperiment + ":" + strconv.FormatInt(agentID, 10)))
	if digest[0]&1 == 0 {
		return "a1"
	}
	return "a2"
}

// ObserveRecommendationRequest records the final buffered HTTP response for an
// authenticated Agent, including empty and failed results. It never reads the
// request body, credentials, private context, or recommendation content.
// Recording the server response does not prove that a remote client received it.
func ObserveRecommendationRequest(ctx context.Context, c *app.RequestContext, agentID, startedAt int64) {
	observation, ok := buildRecommendationObservation(c, agentID, startedAt)
	if !ok {
		return
	}
	metrics.RecommendationAARequests.WithLabelValues(observation.Endpoint, observation.Arm, observation.Outcome).Inc()
	logger.Ctx(ctx).Info("recommendation_aa_observation", "observation", observation)
}

func buildRecommendationObservation(c *app.RequestContext, agentID, startedAt int64) (recommendationObservation, bool) {
	var endpoint string
	switch string(c.Request.Method()) + " " + string(c.Request.URI().Path()) {
	case "GET /api/v1/items/feed":
		endpoint = "feed_v1"
	case "POST /api/v2/feed":
		endpoint = "feed_v2"
	case "POST /api/v2/discovery/recommendations":
		endpoint = "recommendations_v2"
	default:
		return recommendationObservation{}, false
	}
	if agentID <= 0 {
		return recommendationObservation{}, false
	}
	o := recommendationObservation{
		ObservationID: uuid.NewString(), ExperimentID: recommendationAAExperiment, Arm: recommendationAAArm(agentID), AgentID: strconv.FormatInt(agentID, 10),
		Endpoint: endpoint, Caller: "unknown", StartedAt: startedAt, CompletedAt: time.Now().UnixMilli(),
		HTTPStatus: c.Response.StatusCode(), Outcome: "invalid_response", PipelineVersion: "unknown", ResultStatus: "unknown", ResponseInputOrigin: "unknown",
	}
	if len(c.GetHeader("Origin")) > 0 || len(c.GetHeader("Sec-Fetch-Site")) > 0 {
		o.Caller = "browser"
	} else if len(c.GetHeader("X-CLI-Ver")) > 0 {
		o.Caller = "cli"
	}
	if o.HTTPStatus < 200 || o.HTTPStatus >= 300 {
		o.Outcome = "http_error"
	}
	body := c.Response.Body()
	if len(body) > observationBodyLimit {
		return o, true
	}
	var envelope struct {
		Code  *int            `json:"code"`
		Error json.RawMessage `json:"error"`
		Data  json.RawMessage `json:"data"`
		Msg   string          `json:"msg"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return o, true
	}
	if envelope.Code != nil && *envelope.Code != 0 {
		o.ErrorCode = strconv.Itoa(*envelope.Code)
	}
	if len(envelope.Error) > 0 && string(envelope.Error) != "null" {
		var apiError struct {
			Code string `json:"code"`
		}
		if json.Unmarshal(envelope.Error, &apiError) == nil {
			switch apiError.Code {
			case "FEED_REQUEST_IN_PROGRESS", "FEED_SOURCE_UNAVAILABLE", "FEED_V2_UNAVAILABLE", "FEED_READ_FAILED", "FEED_CONTEXT_READ_FAILED", "FEED_PAYLOAD_TOO_LARGE", "AGENT_AUTH_INVALID", "AGENT_SCOPE_REQUIRED", "ONBOARDING_REQUIRED", "INVALID_REQUEST":
				o.ErrorCode = apiError.Code
			}
		}
	}
	if o.HTTPStatus < 200 || o.HTTPStatus >= 300 {
		if o.HTTPStatus == 409 && (o.ErrorCode == "FEED_REQUEST_IN_PROGRESS" || (o.ErrorCode == "409" && envelope.Msg == "request_in_progress")) {
			o.Outcome = "in_progress"
		}
		return o, true
	}
	if (envelope.Code != nil && *envelope.Code != 0) || (len(envelope.Error) > 0 && string(envelope.Error) != "null") {
		o.Outcome = "business_error"
		if o.ErrorCode == "409" && envelope.Msg == "request_in_progress" {
			o.Outcome = "in_progress"
		}
		return o, true
	}
	var data struct {
		Items        json.RawMessage `json:"items"`
		ImpressionID string          `json:"impression_id"`
		recommendationMetadata
		Discovery *recommendationMetadata `json:"discovery"`
	}

	if json.Unmarshal(envelope.Data, &data) != nil || len(data.Items) == 0 {
		return o, true
	}
	var items []json.RawMessage
	if json.Unmarshal(data.Items, &items) != nil {
		return o, true
	}
	count := len(items)
	o.ItemCount = &count
	o.Outcome = "nonempty"
	if count == 0 {
		o.Outcome = "empty"
	}
	// Only opaque numeric impression IDs are retained; unexpected values must
	// never turn response text into a logging channel.
	if id, err := strconv.ParseInt(data.ImpressionID, 10, 64); err == nil && id > 0 {
		o.ImpressionID = strconv.FormatInt(id, 10)
	}
	metadata := data.recommendationMetadata
	if data.Discovery != nil {
		metadata = *data.Discovery
	}
	if metadata.PipelineVersion == "need_search_v1" {
		o.PipelineVersion = metadata.PipelineVersion
	}
	switch metadata.ResultStatus {
	case "ok", "no_match", "exhausted", "insufficient_context", "below_threshold":
		o.ResultStatus = metadata.ResultStatus
	}
	o.Partial = metadata.Partial
	switch metadata.InputOrigin {
	case "need_input", "agent_context", "baseline", "friend":
		o.ResponseInputOrigin = metadata.InputOrigin
	}
	switch metadata.FallbackReason {
	case "no_active_needs", "missing_kind_needs", "empty_agent_context":
		o.FallbackReason = metadata.FallbackReason
	}
	return o, true
}
