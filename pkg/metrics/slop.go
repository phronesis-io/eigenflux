package metrics

import "github.com/prometheus/client_golang/prometheus"

// Attempts include retries. Durable per-item decisions are queried from PostgreSQL.
var UGCSlopAttempts = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "eigenflux_ugc_slop_attempts_total", Help: "UGC extraction attempts by outcome, including retries.",
}, []string{"outcome"})

var UGCSlopEnabled = prometheus.NewGauge(prometheus.GaugeOpts{Name: "eigenflux_ugc_slop_enabled", Help: "Whether new UGC extraction and blocking are enabled in this worker."})

func init() { Registry.MustRegister(UGCSlopAttempts, UGCSlopEnabled) }
