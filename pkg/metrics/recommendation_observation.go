package metrics

import "github.com/prometheus/client_golang/prometheus"

// Labels are bounded endpoint/arm/outcome values, never Agent or impression IDs.
var RecommendationAARequests = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "recommendation_aa_http_requests_total",
	Help: "Authenticated recommendation HTTP completions under identical A/A serving policies, including empty and failed responses.",
}, []string{"endpoint", "arm", "outcome"})

func init() { Registry.MustRegister(RecommendationAARequests) }
