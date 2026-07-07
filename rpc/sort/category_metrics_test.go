package main

import (
	"testing"

	sortDal "eigenflux_server/rpc/sort/dal"
)

func TestCategoryLabels(t *testing.T) {
	cases := []struct {
		name           string
		item           sortDal.Item
		wantBroadcast  string
		wantSourceType string
	}{
		{"supply original", sortDal.Item{Type: "supply", SourceType: "original"}, "supply", "original"},
		{"demand forwarded", sortDal.Item{Type: "demand", SourceType: "forwarded"}, "demand", "forwarded"},
		{"empty broadcast", sortDal.Item{Type: "", SourceType: "curated"}, "none", "curated"},
		{"empty source", sortDal.Item{Type: "info", SourceType: ""}, "info", "none"},
		{"both empty", sortDal.Item{}, "none", "none"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bt, st := categoryLabels(c.item)
			if bt != c.wantBroadcast || st != c.wantSourceType {
				t.Fatalf("categoryLabels(%+v) = (%q, %q), want (%q, %q)",
					c.item, bt, st, c.wantBroadcast, c.wantSourceType)
			}
		})
	}
}
