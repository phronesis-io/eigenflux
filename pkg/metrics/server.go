package metrics

import (
	"fmt"
	"log"
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func StartMetricsServer(port int) {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(Registry, promhttp.HandlerOpts{}))
	addr := fmt.Sprintf(":%d", port)
	log.Printf("metrics server listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Printf("metrics server error: %v", err)
	}
}
