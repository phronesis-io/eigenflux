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

func init() {
	Registry.MustRegister(
		HTTPRequestDuration, HTTPRequestsTotal, HTTPRequestsInFlight,
		RPCRequestDuration, RPCRequestsTotal,
		ConsumerMessagesTotal, ConsumerMessageDuration, ConsumerLag, ConsumerRetryTotal,
	)
}
