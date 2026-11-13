package metrics

import "github.com/prometheus/client_golang/prometheus"

// Labels are fixed values. Request identities are never labels.
var IntegrationRateLimitTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "integration_rate_limit_total",
	Help: "Enterprise request admission decisions and limiter storage failures.",
}, []string{"mode", "lane", "scope", "result"})

func init() {
	Registry.MustRegister(IntegrationRateLimitTotal)
}
