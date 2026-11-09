package main

import (
	"eigenflux_server/pkg/summary"
	"fmt"
)

// ownershipProjection models publication-time format rejection and exact source
// ownership. It never edits the captured production record.
func ownershipProjection(item itemEvidence, class string) (string, bool) {
	if class != "ugc" {
		return item.Summary, false
	}
	text, err := summary.Publisher(item.Notes)
	if err != nil {
		return "", true
	}
	return text, false
}

func ownershipVariantCheck(ownership, coupled, sourceContext bool) {
	if ownership && (coupled || sourceContext) {
		panic(fmt.Errorf("UGC ownership cannot be mixed with PGC correction arms"))
	}
}
