package main

import (
	"testing"

	sortDal "eigenflux_server/rpc/sort/dal"
)

func TestCategoryLabels(t *testing.T) {
	cases := []struct {
		name             string
		item             sortDal.Item
		contentClass     string
		wantBroadcast    string
		wantContentClass string
	}{
		{"supply ugc", sortDal.Item{Type: "supply"}, contentClassUGC, "supply", "ugc"},
		{"demand pgc", sortDal.Item{Type: "demand"}, contentClassPGC, "demand", "pgc"},
		{"empty broadcast", sortDal.Item{Type: ""}, contentClassUGC, "none", "ugc"},
		{"empty content class", sortDal.Item{Type: "info"}, "", "info", "none"},
		{"both empty", sortDal.Item{}, "", "none", "none"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bt, cc := categoryLabels(c.item, c.contentClass)
			if bt != c.wantBroadcast || cc != c.wantContentClass {
				t.Fatalf("categoryLabels(%+v, %q) = (%q, %q), want (%q, %q)",
					c.item, c.contentClass, bt, cc, c.wantBroadcast, c.wantContentClass)
			}
		})
	}
}
