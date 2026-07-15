package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

var Registry = prometheus.NewRegistry()

// HTTP metrics (API gateway, console).
var (
	HTTPRequestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "HTTP request latency in seconds.",
		Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
	}, []string{"method", "path", "status"})

	HTTPRequestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Total number of HTTP requests.",
	}, []string{"method", "path", "status"})

	HTTPRequestsInFlight = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "http_requests_in_flight",
		Help: "Number of HTTP requests currently being processed.",
	})
)

// RPC metrics (Kitex services).
var (
	RPCRequestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "rpc_request_duration_seconds",
		Help:    "RPC request latency in seconds.",
		Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
	}, []string{"service", "method", "status"})

	RPCRequestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "rpc_requests_total",
		Help: "Total number of RPC requests.",
	}, []string{"service", "method", "status"})
)

// Consumer metrics (pipeline).
var (
	ConsumerMessagesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "consumer_messages_processed_total",
		Help: "Total consumer messages processed.",
	}, []string{"stream", "status"})

	ConsumerMessageDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "consumer_message_duration_seconds",
		Help:    "Per-message processing duration in seconds.",
		Buckets: []float64{0.01, 0.05, 0.1, 0.5, 1, 5, 10, 30, 60},
	}, []string{"stream"})

	ConsumerLag = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "consumer_lag",
		Help: "Number of pending messages in consumer group.",
	}, []string{"stream", "consumer_group"})

	ConsumerRetryTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "consumer_retry_total",
		Help: "Total consumer message retries.",
	}, []string{"stream"})
)

// Pipeline processing metrics.
var (
	ItemPublishToProcessDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "item_publish_to_process_duration_seconds",
		Help:    "End-to-end latency from item publish to processing complete.",
		Buckets: []float64{1, 5, 10, 30, 60, 120, 300, 600},
	})
)

// Recall source metrics (sort service).
var (
	RecallImpressionTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "recall_impression_total",
		Help: "Total items impressed (served) by recall source.",
	}, []string{"source"})

	RecallFeedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "recall_feed_total",
		Help: "Total items entering feed by recall source (before dedup).",
	}, []string{"source"})

	// NewUGCInjectedTotal counts un-exposed UGC items force-inserted into a
	// reserved feed slot by InjectPolicy and delivered (survived dedup + limit).
	// This is the "exposure guarantee fired" signal, distinct from
	// recall_impression_total{source="new_ugc_recall"} which also counts
	// new_ugc_recall items that ranked into the feed naturally.
	NewUGCInjectedTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "sort_new_ugc_injected_total",
		Help: "Total un-exposed UGC items force-inserted into a reserved feed slot and delivered.",
	})

	// SortRecallCategoryTotal and SortFeedCategoryTotal track the broadcast_type /
	// content_class mix at the recall and delivered-feed stages. Comparing the two
	// shows how ranking + boost policy + threshold + dedup reshape the category
	// distribution — the signal for whether supply/demand and UGC
	// (content_class=ugc, i.e. non-PGC-bot authors) promotion actually reaches the
	// served feed.
	SortRecallCategoryTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "sort_recall_category_total",
		Help: "Recall candidates by broadcast_type and content_class (before ranking/boost/dedup).",
	}, []string{"broadcast_type", "content_class"})

	SortFeedCategoryTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "sort_feed_category_total",
		Help: "Items delivered to the feed by broadcast_type and content_class (after boost and dedup).",
	}, []string{"broadcast_type", "content_class"})
)

// SearchServices metrics. Volume, sub-intent distribution, LLM-fallback
// rate, and per-phase latency are all tagged so we can spot regressions in
// (a) agent-side decomposition quality, (b) LLM cost, and (c) fan-out timing
// for service search.
var (
	SearchServicesRequestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "sort_search_services_requests_total",
		Help: "SearchServices request volume by sub-intent source.",
	}, []string{"sub_intents_source"})

	SearchServicesSubIntents = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "sort_search_services_sub_intents",
		Help:    "Number of effective sub-intents per SearchServices request.",
		Buckets: []float64{1, 2, 3, 4, 5, 6, 7, 8},
	})

	SearchServicesLLMFallbackTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "sort_search_services_llm_fallback_total",
		Help: "Times the sort server fell back to LLM sub-intent decomposition.",
	}, []string{"reason"})

	SearchServicesLatencyMs = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "sort_search_services_latency_ms",
		Help:    "Per-phase latency for SearchServices, in milliseconds.",
		Buckets: prometheus.ExponentialBuckets(10, 2, 12),
	}, []string{"phase"})

	SearchServicesEmptyTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "sort_search_services_empty_total",
		Help: "SearchServices requests that returned no candidates.",
	})
)

// LLM call metrics.
var (
	LLMCallDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "llm_call_duration_seconds",
		Help:    "LLM API call latency in seconds.",
		Buckets: []float64{0.5, 1, 2, 5, 10, 20, 30, 60},
	}, []string{"prompt"})

	LLMReasoningTokens = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "llm_reasoning_tokens",
		Help:    "Number of reasoning tokens in LLM response.",
		Buckets: []float64{0, 100, 500, 1000, 2000, 5000, 10000},
	}, []string{"prompt"})

	LLMCompletionTokens = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "llm_completion_tokens",
		Help:    "Number of completion tokens in LLM response.",
		Buckets: []float64{50, 100, 200, 500, 1000, 2000, 5000},
	}, []string{"prompt"})
)

func init() {
	Registry.MustRegister(
		HTTPRequestDuration, HTTPRequestsTotal, HTTPRequestsInFlight,
		RPCRequestDuration, RPCRequestsTotal,
		ConsumerMessagesTotal, ConsumerMessageDuration, ConsumerLag, ConsumerRetryTotal,
		ItemPublishToProcessDuration,
		RecallImpressionTotal, RecallFeedTotal, NewUGCInjectedTotal,
		SortRecallCategoryTotal, SortFeedCategoryTotal,
		LLMCallDuration, LLMReasoningTokens, LLMCompletionTokens,
		SearchServicesRequestsTotal, SearchServicesSubIntents, SearchServicesLLMFallbackTotal,
		SearchServicesLatencyMs, SearchServicesEmptyTotal,
	)
}
