package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"eigenflux_server/pkg/config"
	"eigenflux_server/rpc/sort/discovery"
)

type needPage struct {
	Counts                                            map[int64]int
	NeedItems, AbsentNeeds, BelowFloor, MissingCosine int
	TopShare, BelowFloorShare                         float64
}

func summarizeNeedPage(contexts []discovery.Context, candidates []discovery.Candidate, floor float64) needPage {
	page := needPage{Counts: map[int64]int{}}
	for _, c := range contexts {
		if c.CapturedNeed != nil {
			page.Counts[c.ID] = 0
		}
	}
	top := 0
	for _, c := range candidates {
		if c.Context.CapturedNeed == nil || c.Document.Ref.Type != discovery.Broadcast {
			continue
		}
		page.Counts[c.Context.ID]++
		page.NeedItems++
		top = max(top, page.Counts[c.Context.ID])
		if v, ok := discovery.AttributionCosine(c); !ok {
			page.MissingCosine++
		} else if v < floor {
			page.BelowFloor++
		}
	}
	for _, count := range page.Counts {
		if count == 0 {
			page.AbsentNeeds++
		}
	}
	if page.NeedItems > 0 {
		page.TopShare = float64(top) / float64(page.NeedItems)
		page.BelowFloorShare = float64(page.BelowFloor) / float64(page.NeedItems)
	}
	return page
}

// The reference cap can request a different hydration batch. Capture resolves
// the full recall union before recording any per-context batches.
type referenceSource struct {
	*frozenSource
	inventory map[string]discovery.Document
}

func (s referenceSource) Hydrate(_ context.Context, _ int64, _ discovery.Mode, docs []discovery.Document) ([]discovery.Document, error) {
	out := []discovery.Document{}
	for _, d := range docs {
		if v, ok := s.inventory[d.Ref.Key()]; ok {
			out = append(out, v)
		}
	}
	return out, nil
}

func evaluateNeedRelative(cfg *config.Config, dir string) {
	snapshots := loadSnapshots(dir)
	pages := map[int64]map[string]needPage{}
	// Page shares average over pages with Need items; an empty page has no allocation to measure.
	type mean struct {
		TopShare, BelowFloorShare, AbsentNeeds, NeedItems float64
		PagesWithNeedItems                                int
	}
	means := map[string]mean{}
	lost := []int64{}
	for _, s := range snapshots {
		rows := map[string]needPage{}
		for _, arm := range []string{"baseline", "relative", "cosine"} {
			var source discovery.Sources = newFrozenSource(s)
			if arm == "cosine" {
				inventory := map[string]discovery.Document{}
				for _, d := range restoreDocs(s.Inventory) {
					inventory[d.Ref.Key()] = d
				}
				source = referenceSource{newFrozenSource(s), inventory}
			}
			e, closeModel := engine(cfg, dir, s, source)
			if e.NeedMix == nil {
				closeModel()
				panic("need-relative replay requires frozen need_mix policy")
			}
			e.NeedRelativeBPS = 0
			run := e.ReplayContexts
			switch arm {
			case "relative":
				e.NeedRelativeBPS = 10000
			case "cosine":
				run = e.ReplayCosineReference
			}
			got, err := run(context.Background(), s.Owner, s.Contexts, []discovery.Kind{discovery.Broadcast}, 20, s.At)
			closeModel()
			must(err)
			if arm == "baseline" {
				a, err := json.Marshal(got)
				must(err)
				b, err := json.Marshal(s.Before)
				must(err)
				if !bytes.Equal(a, b) {
					panic(fmt.Sprintf("baseline mismatch for Agent %d", s.Owner))
				}
			}
			row := summarizeNeedPage(s.Contexts, got.Candidates, e.NeedMix.MinNeedCosine)
			if len(row.Counts) < 2 {
				panic(fmt.Sprintf("Agent %d requires two captured Needs", s.Owner))
			}
			rows[arm] = row
			m := means[arm]
			if row.NeedItems > 0 {
				m.TopShare += row.TopShare
				m.BelowFloorShare += row.BelowFloorShare
				m.PagesWithNeedItems++
			}
			m.AbsentNeeds += float64(row.AbsentNeeds)
			m.NeedItems += float64(row.NeedItems)
			means[arm] = m
		}
		pages[s.Owner] = rows
		if rows["baseline"].NeedItems > 0 && rows["relative"].NeedItems == 0 {
			lost = append(lost, s.Owner)
		}
	}
	n := float64(len(snapshots))
	for arm, sum := range means {
		withItems := float64(max(sum.PagesWithNeedItems, 1))
		means[arm] = mean{sum.TopShare / withItems, sum.BelowFloorShare / withItems, sum.AbsentNeeds / n, sum.NeedItems / n, sum.PagesWithNeedItems}
	}
	base, treatment := means["baseline"], means["relative"]
	passed := treatment.TopShare < base.TopShare && treatment.BelowFloorShare <= base.BelowFloorShare+.05 && len(lost) == 0
	save(filepath.Join(dir, "need-relative.json"), map[string]any{"agents": pages, "means": means, "lost_all_need_items": lost, "passed": passed})
	b, err := json.Marshal(means)
	must(err)
	fmt.Printf("Cohort means (%d Agents): %s\n", len(snapshots), b)
	if !passed {
		panic("need-relative offline gate failed; see need-relative.json")
	}
}
