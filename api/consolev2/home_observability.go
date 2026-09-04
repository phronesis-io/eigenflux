package consolev2

import (
	"time"

	"eigenflux_server/pkg/metrics"
)

const homeRefreshTimeout = 10 * time.Second

func recordHomeCache(module, result string) {
	metrics.ConsoleHomeCacheTotal.WithLabelValues(module, result).Inc()
}

func observeHomeRefresh(module string, started time.Time, err error) {
	result := "success"
	if err != nil {
		result = "error"
	}
	metrics.ConsoleHomeRefreshDuration.WithLabelValues(module, result).Observe(time.Since(started).Seconds())
}
