package metrics

import "github.com/prometheus/client_golang/prometheus"

// Labels contain bounded modes, source kinds and evaluator reason codes only.
var (
	DiscoveryRecordingFailures = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "discovery_recording_failures_total", Help: "Failed best-effort discovery history or sample writes."}, []string{"stage"})
	DiscoveryTaxonomyMisses    = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "discovery_taxonomy_misses_total", Help: "Contexts with unmapped soft intents."}, []string{"origin"})
	DiscoveryDuration          = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "discovery_execution_seconds", Help: "Discovery execution latency by result status.", Buckets: prometheus.DefBuckets}, []string{"mode", "status"})
	DiscoveryRejected          = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "discovery_rejected_total", Help: "Rejected hydrated discovery candidates."}, []string{"kind", "reason"})
	DiscoveryChannelFailures   = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "discovery_channel_failures_total", Help: "Failed optional recall channels."}, []string{"kind", "channel"})
	DiscoveryFallback          = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "discovery_fallback_total", Help: "Explicit automatic context fallbacks."}, []string{"reason"})
)

func init() {
	Registry.MustRegister(DiscoveryRecordingFailures, DiscoveryTaxonomyMisses, DiscoveryDuration, DiscoveryRejected, DiscoveryChannelFailures, DiscoveryFallback)
}
