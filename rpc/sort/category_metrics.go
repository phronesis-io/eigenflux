package main

import (
	"eigenflux_server/pkg/metrics"
	sortDal "eigenflux_server/rpc/sort/dal"
)

// categoryLabels maps an item's boost-relevant category fields to Prometheus
// label values. Empty broadcast_type collapses to "none" so the label set stays
// bounded to the DB CHECK domain (broadcast_type ∈ {supply,demand,info,alert}).
// contentClass is "ugc"/"pgc" resolved from the author's email suffix; empty
// collapses to "none".
func categoryLabels(item sortDal.Item, contentClass string) (broadcastType, cc string) {
	broadcastType = item.Type
	if broadcastType == "" {
		broadcastType = "none"
	}
	cc = contentClass
	if cc == "" {
		cc = "none"
	}
	return broadcastType, cc
}

func recordRecallCategory(item sortDal.Item, contentClass string) {
	bt, cc := categoryLabels(item, contentClass)
	metrics.SortRecallCategoryTotal.WithLabelValues(bt, cc).Inc()
}

func recordFeedCategory(item sortDal.Item, contentClass string) {
	bt, cc := categoryLabels(item, contentClass)
	metrics.SortFeedCategoryTotal.WithLabelValues(bt, cc).Inc()
}
