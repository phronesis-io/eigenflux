package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"eigenflux_server/pkg/featureindex"
	"eigenflux_server/rpc/sort/discovery"
	"gorm.io/gorm"
)

type itemEvidence struct {
	PotentiallyEligible bool   `json:"potentially_eligible"`
	ItemID              int64  `json:"id,string"`
	Content             string `json:"content"`
	Notes               string `json:"notes"`
	Summary             string `json:"summary"`
	Keywords            string `json:"keywords"`
	ExpireTime          string `json:"expire_time"`
	Lang                string `json:"lang"`
	AuthorID            int64  `json:"author_id,string"`
}

// Export the entire recalled union, including candidates dropped by merge caps,
// hard filters, seen filtering and ranking. No delivery logs stand in for a pool.
func exportEvidence(db *gorm.DB, dir string) {
	files, err := filepath.Glob(filepath.Join(dir, "agent-*.json"))
	must(err)
	ids := map[int64]bool{}
	eligible := map[int64]bool{}
	authority := map[int64]discovery.Document{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		must(err)
		var s snapshot
		must(json.Unmarshal(b, &s))
		if s.CaptureError != "" {
			continue
		}
		for _, d := range restoreDocs(s.Inventory) {
			authority[d.Ref.ID] = d
			if s.Seen[d.Ref.Key()] {
				continue
			}
			for _, c := range s.Contexts {
				c.Filters.ExcludeTerms = nil
				if discovery.Check(c, d, discovery.Recommendation, s.At) == "" {
					eligible[d.Ref.ID] = true
					break
				}
			}
		}
		for _, ch := range s.Channels {
			for _, d := range ch.Documents {
				ids[d.Ref.ID] = true
			}
		}
	}
	sorted := make([]int64, 0, len(ids))
	for id := range ids {
		sorted = append(sorted, id)
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	all := []itemEvidence{}
	var cached []itemEvidence
	if b, e := os.ReadFile(filepath.Join(dir, "items.json")); e == nil {
		must(json.Unmarshal(b, &cached))
	}
	cache := map[int64]itemEvidence{}
	for _, it := range cached {
		d, ok := authority[it.ItemID]
		if ok && d.Version == featureindex.BroadcastContentHash(it.ItemID, d.AuthorID, it.Content+"\n"+it.Summary, d.Slots) {
			it.PotentiallyEligible = eligible[it.ItemID]
			cache[it.ItemID] = it
		}
	}
	pending := []int64{}
	for _, id := range sorted {
		if it, ok := cache[id]; ok {
			all = append(all, it)
		} else {
			pending = append(pending, id)
		}
	}
	fmt.Printf("reusing %d version-verified source records; fetching %d\n", len(all), len(pending))
	sorted = pending
	for i := 0; i < len(sorted); i += 200 {
		end := i + 200
		if end > len(sorted) {
			end = len(sorted)
		}
		var rows []itemEvidence
		must(db.WithContext(context.Background()).Raw(`SELECT r.item_id,r.author_agent_id AS author_id,r.raw_content AS content,r.raw_notes AS notes,p.summary,p.keywords,p.expire_time,p.lang FROM raw_items r JOIN processed_items p USING(item_id) WHERE r.item_id IN ?`, sorted[i:end]).Scan(&rows).Error)
		for i := range rows {
			rows[i].PotentiallyEligible = eligible[rows[i].ItemID]
		}
		all = append(all, rows...)
		fmt.Printf("source evidence %d / %d\n", end, len(sorted))
	}
	save(filepath.Join(dir, "items.json"), all)
	fmt.Printf("exported %d / %d recalled source items\n", len(all), len(ids))
}

// Retain the public origin vocabulary rather than forcing friend/baseline into
// Profile or treating every context ID as a Need.
func basis(c discovery.Context) string {
	switch c.Origin {
	case "need_input":
		return "need"
	case "agent_context":
		return "profile"
	case "friend":
		return "friend"
	case "baseline":
		return "baseline"
	default:
		return "unknown"
	}
}
