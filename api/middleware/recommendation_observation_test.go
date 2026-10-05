package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"eigenflux_server/pkg/metrics"
	"github.com/cloudwego/hertz/pkg/app"
	dto "github.com/prometheus/client_model/go"
)

func TestRecommendationObservationResponseBoundaries(t *testing.T) {
	tests := []struct {
		name, method, path, body, outcome, pipeline, result string
		status                                              int
		count                                               int
		included                                            bool
	}{
		{"v1 empty", "GET", "/api/v1/items/feed", `{"code":0,"data":{"items":[],"impression_id":"365412762643333120"}}`, "empty", "unknown", "unknown", 200, 0, true},
		{"v2 nonempty", "POST", "/api/v2/feed", `{"data":{"items":[{"private":"never log"}],"impression_id":"365412762643333120","discovery":{"pipeline_version":"need_search_v1","result_status":"ok","partial":true,"input_origin":"need_input","fallback_reason":"missing_kind_needs"}}}`, "nonempty", "need_search_v1", "ok", 200, 1, true},
		{"recommendation empty", "POST", "/api/v2/discovery/recommendations", `{"code":0,"data":{"items":[],"pipeline_version":"need_search_v1","result_status":"exhausted"}}`, "empty", "need_search_v1", "exhausted", 200, 0, true},

		{"V1 concurrent request", "GET", "/api/v1/items/feed", `{"code":409,"msg":"request_in_progress"}`, "in_progress", "unknown", "unknown", 200, -1, true},
		{"V2 concurrent request", "POST", "/api/v2/feed", `{"error":{"code":"FEED_REQUEST_IN_PROGRESS"}}`, "in_progress", "unknown", "unknown", 409, -1, true},
		{"unrelated conflict", "POST", "/api/v2/feed", `{"error":{"code":"ONBOARDING_REQUIRED"}}`, "http_error", "unknown", "unknown", 409, -1, true},
		{"source outage", "POST", "/api/v2/feed", `{"error":{"code":"FEED_SOURCE_UNAVAILABLE"}}`, "http_error", "unknown", "unknown", 503, -1, true},
		{"HTTP failure", "POST", "/api/v2/feed", `{"error":{"message":"private context"}}`, "http_error", "unknown", "unknown", 503, -1, true},
		{"scope failure", "POST", "/api/v2/discovery/recommendations", `{}`, "http_error", "unknown", "unknown", 403, -1, true},
		{"business failure", "GET", "/api/v1/items/feed", `{"code":500,"data":{"items":[]}}`, "business_error", "unknown", "unknown", 200, -1, true},
		{"v2 business failure", "POST", "/api/v2/feed", `{"error":{"code":"ERR"}}`, "business_error", "unknown", "unknown", 200, -1, true},
		{"bad JSON", "POST", "/api/v2/feed", `{`, "invalid_response", "unknown", "unknown", 200, -1, true},
		{"missing items", "POST", "/api/v2/feed", `{"data":{}}`, "invalid_response", "unknown", "unknown", 200, -1, true},
		{"wrong items type", "POST", "/api/v2/feed", `{"data":{"items":{}}}`, "invalid_response", "unknown", "unknown", 200, -1, true},
		{"oversized response", "POST", "/api/v2/feed", strings.Repeat("x", observationBodyLimit+1), "invalid_response", "unknown", "unknown", 200, -1, true},
		{"explicit search excluded", "POST", "/api/v2/discovery/search", `{}`, "", "", "", 200, -1, false},
		{"console excluded", "GET", "/api/v2/console/today", `{}`, "", "", "", 200, -1, false},
		{"wrong method", "GET", "/api/v2/feed", `{}`, "", "", "", 200, -1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := app.NewContext(0)
			c.Request.SetMethod(tt.method)
			c.Request.SetRequestURI(tt.path)
			c.Response.SetStatusCode(tt.status)
			c.Response.SetBodyString(tt.body)
			o, ok := buildRecommendationObservation(c, 365412762643333120, 1234)
			if ok != tt.included {
				t.Fatalf("included=%v want=%v", ok, tt.included)
			}
			if !ok {
				return
			}
			if o.Outcome != tt.outcome || o.PipelineVersion != tt.pipeline || o.ResultStatus != tt.result {
				t.Fatalf("observation=%+v", o)
			}
			if tt.count < 0 {
				if o.ItemCount != nil {
					t.Fatal("unknown count must not be represented as zero")
				}
			} else if o.ItemCount == nil || *o.ItemCount != tt.count {
				t.Fatalf("count=%v", o.ItemCount)
			}
			if o.AgentID != "365412762643333120" || o.StartedAt != 1234 || o.ObservationID == "" {
				t.Fatalf("identity/timing=%+v", o)
			}
			if tt.name == "V1 concurrent request" && o.ErrorCode != "409" {
				t.Fatalf("legacy conflict not attributed: %+v", o)
			}
			if tt.name == "V2 concurrent request" && o.ErrorCode != "FEED_REQUEST_IN_PROGRESS" {
				t.Fatalf("V2 conflict not attributed: %+v", o)
			}
			if tt.name == "source outage" && o.ErrorCode != "FEED_SOURCE_UNAVAILABLE" {
				t.Fatalf("source failure not attributed: %+v", o)
			}
			if tt.name == "v2 nonempty" && (o.ResponseInputOrigin != "need_input" || o.FallbackReason != "missing_kind_needs") {
				t.Fatalf("context markers missing: %+v", o)
			}
			encoded, _ := json.Marshal(o)
			if bytes.Contains(encoded, []byte("private")) || bytes.Contains(encoded, []byte("never log")) {
				t.Fatal("private response escaped observation boundary")
			}
		})
	}
}

func TestRecommendationAAStableAgentAllocation(t *testing.T) {
	// Fixed vectors are shared with the offline diagnostic, not request order,
	// client versions, experiment counters or process-local random state.
	for _, id := range []int64{1, 2, 365412762643333120} {
		before := recommendationAAArm(id)
		for range 20 {
			if recommendationAAArm(id) != before {
				t.Fatal("unstable assignment")
			}
		}
	}
	if recommendationAAArm(1) != "a2" || recommendationAAArm(2) != "a2" || recommendationAAArm(365412762643333120) != "a1" {
		t.Fatal("allocation contract changed")
	}
	c := app.NewContext(0)
	c.Request.SetMethod("POST")
	c.Request.SetRequestURI("/api/v2/feed")
	c.Response.SetBodyString(`{"data":{"items":[]}}`)
	if _, ok := buildRecommendationObservation(c, 0, 0); ok {
		t.Fatal("unauthenticated request enrolled")
	}
	c.Request.Header.Set("X-CLI-Ver", "untrusted arbitrary text")
	o, _ := buildRecommendationObservation(c, 1, 0)
	if o.Caller != "cli" {
		t.Fatal("CLI cohort absent")
	}
	c.Request.Header.Set("Origin", "https://console.example.test")
	o, _ = buildRecommendationObservation(c, 1, 0)
	if o.Caller != "browser" {
		t.Fatal("browser cannot be classified as CLI")
	}
	c.Response.SetBodyString(`{"data":{"items":[],"impression_id":"private text","discovery":{"pipeline_version":"private text","result_status":"private text"}}}`)
	o, _ = buildRecommendationObservation(c, 1, 0)
	if o.ImpressionID != "" || o.PipelineVersion != "unknown" || o.ResultStatus != "unknown" {
		t.Fatal("unbounded response metadata logged")
	}
}

func TestRecommendationObservationEmitsCompletionAndCounter(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	defer slog.SetDefault(previous)
	c := app.NewContext(0)
	c.Request.SetMethod("POST")
	c.Request.SetRequestURI("/api/v2/feed")
	c.Response.SetBodyString(`{"data":{"items":[]}}`)
	counter := metrics.RecommendationAARequests.WithLabelValues("feed_v2", recommendationAAArm(123), "empty")
	var metric dto.Metric
	if err := counter.Write(&metric); err != nil {
		t.Fatal(err)
	}
	before := metric.GetCounter().GetValue()
	ObserveRecommendationRequest(context.Background(), c, 123, 1234)
	if err := counter.Write(&metric); err != nil {
		t.Fatal(err)
	}
	if metric.GetCounter().GetValue() != before+1 {
		t.Fatal("completion counter missing")
	}
	var entry map[string]json.RawMessage
	if json.Unmarshal(output.Bytes(), &entry) != nil || entry["observation"] == nil {
		t.Fatalf("missing structured observation: %s", output.String())
	}
}
