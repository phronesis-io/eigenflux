package main

import (
	"context"
	"eigenflux_server/pipeline/llm"
	"eigenflux_server/pkg/config"
	"eigenflux_server/rpc/sort/discovery"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

type slopFrozenSource struct {
	*frozenSource
	decisions  map[int64]*llm.SlopEvaluation
	rolloutBPS int
}

func (s *slopFrozenSource) Hydrate(ctx context.Context, owner int64, mode discovery.Mode, docs []discovery.Document) ([]discovery.Document, error) {
	out, err := s.frozenSource.Hydrate(ctx, owner, mode, docs)
	if err != nil {
		return nil, err
	}
	for i := range out {
		d := &out[i]
		if s.snap.Classes[d.Ref.ID] != "ugc" || !config.SlopAuthorInRollout(d.AuthorID, s.rolloutBPS) {
			continue
		}
		e := s.decisions[d.Ref.ID]
		if e == nil {
			continue
		} // coverage is checked before execution
		if e.Result.Discard || (e.Result.IsSlop != nil && *e.Result.IsSlop == 1) {
			d.Blocked = true
			d.Quality = 0
		}
	}
	return out, nil
}

// slopReplay measures the admission gate on complete captured recall pools.
// It does not claim to replay lexical/embedding or global model replacement.
func slopReplay(cfg *config.Config, dir string) {
	replayAll(cfg, dir) // exact A/A reproduction is mandatory
	decisions := map[int64]*llm.SlopEvaluation{}
	files, err := filepath.Glob(filepath.Join(dir, "slop-evaluations", "*.json"))
	must(err)
	for _, f := range files {
		var r struct {
			ID         int64               `json:"id,string"`
			Evaluation *llm.SlopEvaluation `json:"evaluation"`
		}
		b, err := os.ReadFile(f)
		must(err)
		must(json.Unmarshal(b, &r))
		if r.Evaluation != nil {
			decisions[r.ID] = r.Evaluation
		}
	}
	var items []itemEvidence
	b, err := os.ReadFile(filepath.Join(dir, "items.json"))
	must(err)
	must(json.Unmarshal(b, &items))
	evidence := map[int64]itemEvidence{}
	for _, it := range items {
		evidence[it.ItemID] = it
	}
	manifest := []map[string]any{}
	for _, s := range loadSnapshots(dir) {
		required := map[int64]bool{}
		for id, class := range s.Classes {
			it, exists := evidence[id]
			if class != "ugc" {
				continue
			}
			if !exists {
				panic(fmt.Sprintf("missing source evidence %d", id))
			}
			if !it.PotentiallyEligible || !config.SlopAuthorInRollout(it.AuthorID, cfg.UGCSlopRolloutBPS) {
				continue
			}
			required[id] = true
			e := decisions[id]
			if e == nil || e.RequestContract != llm.SlopRequestContract || e.ContentHash != llm.SlopHash(it.Content+"\x00"+it.Notes) {
				panic(fmt.Sprintf("missing matching Slop evaluation %d", id))
			}
		}
		source := &slopFrozenSource{frozenSource: newFrozenSource(s), decisions: decisions, rolloutBPS: cfg.UGCSlopRolloutBPS}
		e, close := engine(cfg, dir, s, source)
		after, err := e.ReplayContexts(context.Background(), s.Owner, s.Contexts, []discovery.Kind{discovery.Broadcast}, 20, s.At)
		close()
		must(err)
		save(filepath.Join(dir, fmt.Sprintf("comparison-slop-%d.json", s.Owner)), map[string]any{"before": s.Before, "after": after})
		manifest = append(manifest, map[string]any{"agent_id": strconv.FormatInt(s.Owner, 10), "required_ugc": len(required), "pool": len(s.Inventory), "captured_delivered": len(s.Before.Candidates), "before": len(s.Before.Candidates), "after": len(after.Candidates)})
		fmt.Printf("agent %d: before=%d after=%d UGC evaluations=%d\n", s.Owner, len(s.Before.Candidates), len(after.Candidates), len(required))
	}
	save(filepath.Join(dir, "counterfactual-slop-manifest.json"), map[string]any{"method": fmt.Sprintf("publisher rollout %d/10000; gate-only within frozen complete recall pools; no global model/lexical/embedding replacement; steady-state sensitivity, not a historical backfill", cfg.UGCSlopRolloutBPS), "agents": manifest})
}
