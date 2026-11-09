package main

import (
	"eigenflux_server/pkg/summary"
	"eigenflux_server/rpc/sort/discovery"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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

type ownershipGrouping struct {
	InputHash     string `json:"input_hash"`
	SourceVersion string `json:"source_version"`
	Kind          string `json:"kind"`
	Policy        string `json:"policy"`
	GroupID       int64  `json:"group_id,string"`
	Error         string `json:"error"`
}

func loadOwnershipGroups(dir string) map[int64]ownershipGrouping {
	rows := map[int64]ownershipGrouping{}
	b, err := os.ReadFile(filepath.Join(dir, "ownership-neighbors.json"))
	if os.IsNotExist(err) {
		return rows
	}
	must(err)
	must(json.Unmarshal(b, &rows))
	return rows
}

func ownershipGroup(row ownershipGrouping, item itemEvidence, d discovery.Document, kind string) (int64, error) {
	if row.Error != "" || row.GroupID <= 0 || row.InputHash != auditInputHash(item) || row.SourceVersion != d.Version || row.Kind != kind || row.Policy != "ugc-grouping-v1-f32" {
		return 0, fmt.Errorf("missing/stale UGC grouping sensitivity for %d", item.ItemID)
	}
	return row.GroupID, nil
}

// Language and expiry are model outputs, so their existing values must not
// exclude a candidate from the metadata sensitivity. Immutable visibility,
// author constraints and already-consumed items still apply.
func ownershipCandidates(snapshots []*snapshot) map[int64]bool {
	out := map[int64]bool{}
	for _, s := range snapshots {
		for _, d := range restoreDocs(s.Inventory) {
			if s.Classes[d.Ref.ID] != "ugc" || s.Seen[d.Ref.Key()] {
				continue
			}
			d.ExpiresAt = 0
			for _, c := range s.Contexts {
				c.Filters.Lang = nil
				c.Filters.ExcludeTerms = nil
				if discovery.Check(c, d, discovery.Recommendation, s.At) == "" {
					out[d.Ref.ID] = true
					break
				}
			}
		}
	}
	return out
}

func ownershipWorkers(value string) (int, error) {
	if value == "" {
		return 4, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 || n > 16 {
		return 0, fmt.Errorf("FIDELITY_MODEL_WORKERS must be between 1 and 16")
	}
	return n, nil
}

func ownershipVariantCheck(ownership, coupled, sourceContext bool) {
	if ownership && (coupled || sourceContext) {
		panic(fmt.Errorf("UGC ownership cannot be mixed with PGC correction arms"))
	}
}
