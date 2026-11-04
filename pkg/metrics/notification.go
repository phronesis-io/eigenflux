package metrics

import "github.com/prometheus/client_golang/prometheus"

// Boundary labels are fixed call sites; payloads and identifiers are never labels.
var ModerationNotificationSkippedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "moderation_notification_skipped_total",
	Help: "Invalid moderation notification delivery attempts skipped at each validation boundary; repeated reads count again.",
}, []string{"boundary"})

func init() {
	Registry.MustRegister(ModerationNotificationSkippedTotal)
}
