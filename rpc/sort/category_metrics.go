package main

import (
	"eigenflux_server/pkg/metrics"
	sortDal "eigenflux_server/rpc/sort/dal"
)

// categoryLabels maps an item's boost-relevant category fields to Prometheus
// label values. Empty broadcast_type or source_type collapse to "none" so the
// label set stays bounded to the DB CHECK domains (broadcast_type ∈
// {supply,demand,info,alert}, source_type ∈ {original,curated,forwarded}).
func categoryLabels(item sortDal.Item) (broadcastType, sourceType string) {
	broadcastType = item.Type
	if broadcastType == "" {
		broadcastType = "none"
	}
	sourceType = item.SourceType
	if sourceType == "" {
		sourceType = "none"
	}
	return broadcastType, sourceType
}

func recordRecallCategory(item sortDal.Item) {
	bt, st := categoryLabels(item)
	metrics.SortRecallCategoryTotal.WithLabelValues(bt, st).Inc()
}

func recordFeedCategory(item sortDal.Item) {
	bt, st := categoryLabels(item)
	metrics.SortFeedCategoryTotal.WithLabelValues(bt, st).Inc()
}
