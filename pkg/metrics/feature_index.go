package metrics

import "github.com/prometheus/client_golang/prometheus"

// Feature labels are registered view names and fixed operation/outcome values.
// IDs, query text and concrete index generations must never be metric labels.
var (
	FeatureKeys              = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "feature_index_keys", Help: "Physical keys observed in the last completed background scan; approximate during keyspace changes."}, []string{"type", "view"})
	FeatureKeyScanCompleted  = prometheus.NewGauge(prometheus.GaugeOpts{Name: "feature_index_key_scan_completed_timestamp_seconds", Help: "Completion timestamp of the published shared key census; zero until the first complete scan."})
	FeatureReadItems         = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "feature_index_read_items_total", Help: "Feature component read outcomes."}, []string{"type", "view", "outcome"})
	FeatureWriteItems        = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "feature_index_write_items_total", Help: "Accepted, stale and failed feature writes."}, []string{"view", "outcome"})
	FeatureLoaderSteps       = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "feature_index_loader_steps_total", Help: "Source loader page outcomes."}, []string{"view", "outcome"})
	FeatureLoaderLastSuccess = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "feature_index_loader_last_success_timestamp_seconds", Help: "Unix timestamp of the last successfully checkpointed page."}, []string{"view"})
	FeatureLoaderLastCycle   = prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "feature_index_loader_last_cycle_timestamp_seconds", Help: "Unix timestamp of the last completed source scan."}, []string{"view"})
	FeatureAudit             = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "feature_index_audit_total", Help: "Bounded background retention audit outcomes."}, []string{"outcome"})
)

func init() {
	Registry.MustRegister(FeatureKeys, FeatureKeyScanCompleted, FeatureReadItems, FeatureWriteItems, FeatureLoaderSteps, FeatureLoaderLastSuccess, FeatureLoaderLastCycle, FeatureAudit)
}
