package metrics

import "github.com/prometheus/client_golang/prometheus"

// Labels contain bounded modes, source kinds and evaluator reason codes only.
var (
	DiscoveryContextCache      = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "discovery_context_cache_total", Help: "Discovery input and compiled-value cache outcomes."}, []string{"scope", "outcome"})
	DiscoveryNeedEmbedding     = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "discovery_need_embedding_total", Help: "Need vector cache and precomputation outcomes."}, []string{"operation", "outcome"})
	DiscoveryRecordingFailures = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "discovery_recording_failures_total", Help: "Failed best-effort discovery history or sample writes."}, []string{"stage"})
	DiscoveryDuration          = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "discovery_execution_seconds", Help: "Discovery execution latency by result status.", Buckets: prometheus.DefBuckets}, []string{"mode", "status"})
	DiscoveryRejected          = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "discovery_rejected_total", Help: "Rejected hydrated discovery candidates."}, []string{"kind", "reason"})
	DiscoveryChannelFailures   = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "discovery_channel_failures_total", Help: "Failed optional recall channels."}, []string{"kind", "channel"})
	DiscoveryFallback          = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "discovery_fallback_total", Help: "Explicit automatic context fallbacks."}, []string{"reason"})
)

func init() {
	Registry.MustRegister(DiscoveryContextCache, DiscoveryNeedEmbedding, DiscoveryRecordingFailures, DiscoveryDuration, DiscoveryRejected, DiscoveryChannelFailures, DiscoveryFallback)
}
