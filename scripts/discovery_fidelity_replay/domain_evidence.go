package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Late read-only domain evidence is usable only when every captured source
// field still matches. It must not silently refresh the frozen experiment.
func bindDomainEvidence(dir string, items []itemEvidence) []itemEvidence {
	raw, err := os.ReadFile(filepath.Join(dir, "retrieval-domain-source.json"))
	must(err)
	var rows []itemEvidence
	must(json.Unmarshal(raw, &rows))
	byID := map[int64]itemEvidence{}
	for _, r := range rows {
		if _, ok := byID[r.ItemID]; ok {
			panic("duplicate domain evidence")
		}
		byID[r.ItemID] = r
	}
	out := append([]itemEvidence(nil), items...)
	for i, item := range out {
		r, ok := byID[item.ItemID]
		if !ok || r.Content != item.Content || r.Notes != item.Notes || r.Summary != item.Summary || r.Keywords != item.Keywords || r.Lang != item.Lang || r.ExpireTime != item.ExpireTime {
			panic(fmt.Sprintf("missing/drifted domain evidence for %d", item.ItemID))
		}
		out[i].Domains = r.Domains
	}
	return out
}
