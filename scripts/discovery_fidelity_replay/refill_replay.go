package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/metrics"
	"eigenflux_server/rpc/sort/discovery"
)

type sequentialSource struct {
	snap           *refillSnapshot
	inventory      map[string]discovery.Document
	consumed       map[int64]bool
	deliveredItems map[int64]bool
	poolIncomplete bool
	poolReasons    map[string]bool
	mu             sync.Mutex
}

func newSequentialSource(s *refillSnapshot) *sequentialSource {
	f := &sequentialSource{snap: s, inventory: map[string]discovery.Document{}, consumed: map[int64]bool{}, deliveredItems: map[int64]bool{}, poolReasons: map[string]bool{}}
	for _, d := range restoreDocs(s.Inventory) {
		f.inventory[d.Ref.Key()] = d
	}
	return f
}
func (s *sequentialSource) Owner(context.Context, int64) (discovery.OwnerContext, error) {
	return discovery.OwnerContext{}, fmt.Errorf("uncaptured owner")
}
func (s *sequentialSource) Recall(_ context.Context, c discovery.Context, k discovery.Kind, ch string, limit int) ([]discovery.Document, error) {
	call, ok := s.snap.Channels[channelKey(c, k, ch)]
	if !ok && ch == "swing_i2i" {
		for _, context := range s.snap.Contexts {
			if candidate, found := s.snap.Channels[channelKey(context, k, ch)]; found {
				call, ok = candidate, true
				break
			}
		}
	}
	if !ok || call.Limit != limit {
		return nil, fmt.Errorf("uncaptured channel %s", ch)
	}
	return restoreDocs(call.Documents), nil
}
func (s *sequentialSource) Hydrate(_ context.Context, _ int64, _ discovery.Mode, docs []discovery.Document) ([]discovery.Document, error) {
	out := []discovery.Document{}
	for _, d := range docs {
		if v, ok := s.inventory[d.Ref.Key()]; ok {
			out = append(out, v)
		}
	}
	return out, nil
}
func (s *sequentialSource) Seen(_ context.Context, _ int64, docs []discovery.Document) (map[string]bool, error) {
	out := map[string]bool{}
	for _, d := range docs {
		seen, ok := s.snap.Seen[d.Ref.Key()]
		if !ok {
			return nil, fmt.Errorf("uncaptured seen fact")
		}
		out[d.Ref.Key()] = seen || s.consumed[d.GroupID]
	}
	return out, nil
}
func (s *sequentialSource) ConsumedGroups(context.Context, int64) ([]int64, bool, error) {
	if s.snap.HistoryError {
		return nil, false, fmt.Errorf("captured history unavailable")
	}
	confirmed := map[int64]bool{}
	for _, id := range s.snap.Confirmed {
		confirmed[id] = true
	}
	history := map[int64]bool{}
	for _, id := range s.snap.History {
		history[id] = true
	}
	for id := range s.consumed {
		history[id] = true
		confirmed[id] = true
	}
	ids := []int64{}
	for id := range history {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] > ids[j] })
	complete := s.snap.HistoryComplete && len(ids) <= discovery.MaxRefillGroups
	if len(ids) > discovery.MaxRefillGroups {
		ids = ids[:discovery.MaxRefillGroups]
	}
	groups := []int64{}
	for _, id := range ids {
		if confirmed[id] {
			groups = append(groups, id)
		}
	}
	return groups, complete, nil
}
func (s *sequentialSource) RecallUnseen(_ context.Context, c discovery.Context, groups, ids []int64, limit int) ([]discovery.Document, error) {
	excluded := map[int64]bool{}
	for _, id := range groups {
		excluded[id] = true
	}
	items := map[int64]bool{}
	for _, id := range ids {
		items[id] = true
	}
	incomplete := false
	reasons := []string{}
	for _, id := range s.snap.Confirmed {
		if !excluded[id] {
			incomplete = true
			reasons = append(reasons, "history_prefix_changed")
			break
		}
	}
	out := []discovery.Document{}
	for _, d := range restoreDocs(s.snap.RefillPools[c.ID]) {
		if !excluded[d.GroupID] && !items[d.Ref.ID] {
			out = append(out, d)
			if len(out) == limit {
				break
			}
		}
	}
	if len(out) < limit && s.snap.Truncated[c.ID] {
		incomplete = true
		reasons = append(reasons, "capture_depth")
	}
	if incomplete {
		s.mu.Lock()
		s.poolIncomplete = true
		for _, reason := range reasons {
			s.poolReasons[reason] = true
		}
		s.mu.Unlock()
	}
	return out, nil
}
func (s *sequentialSource) consume(c discovery.Candidate) error {
	d := c.Document
	if s.deliveredItems[d.Ref.ID] || d.GroupID > 0 && s.consumed[d.GroupID] || s.snap.Seen[d.Ref.Key()] {
		return fmt.Errorf("duplicate or initially consumed delivery")
	}
	s.deliveredItems[d.Ref.ID] = true
	if d.GroupID > 0 {
		s.consumed[d.GroupID] = true
	}
	return nil
}
func rejectionCounters() map[string]float64 {
	families, err := metrics.Registry.Gather()
	must(err)
	out := map[string]float64{}
	for _, family := range families {
		if family.GetName() != "discovery_rejected_total" {
			continue
		}
		for _, m := range family.Metric {
			key := ""
			for _, label := range m.Label {
				if label.GetName() == "reason" {
					key = label.GetValue()
				}
			}
			out[key] += m.GetCounter().GetValue()
		}
	}
	return out
}

type refillStep struct {
	Rejections     map[string]float64
	Step           int
	Delivered      []discovery.Candidate
	Refill         *discovery.RefillReport
	Status         string
	PartialReasons []string
}
type refillArm struct {
	Steps                 []refillStep
	PoolIncomplete        bool
	PoolIncompleteReasons []string
	Rejections            map[string]float64
}
type refillComparison struct {
	Owner         int64
	Before, After refillArm
}

func replayRefill(cfg *config.Config, dir string) {
	b, err := os.ReadFile(filepath.Join(dir, "cohort.json"))
	must(err)
	var cohort []struct{ AgentID int64 }
	must(json.Unmarshal(b, &cohort))
	if len(cohort) != 225 {
		panic("expected 225 Agents")
	}
	snapshotHashes := []string{}
	snapshots := []*refillSnapshot{}
	comparisons := []refillComparison{}
	owners := map[int64]bool{}
	for _, a := range cohort {
		if a.AgentID <= 0 || owners[a.AgentID] {
			panic("invalid or duplicate cohort Agent")
		}
		owners[a.AgentID] = true
		b, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("refill-agent-%d.json", a.AgentID)))
		must(err)
		snapshotHashes = append(snapshotHashes, fmt.Sprintf("%x", sha256.Sum256(b)))
		var s refillSnapshot
		must(json.Unmarshal(b, &s))
		if s.Owner != a.AgentID || s.CaptureFormat != refillCaptureFormat {
			panic("capture identity or format mismatch")
		}
		if s.CaptureError != "" {
			panic(fmt.Sprintf("incomplete capture for Agent: %s", s.CaptureError))
		}
		for i := range s.Contexts {
			s.Contexts[i].Vector = s.Vectors[s.Contexts[i].ID]
		}
		baseline, close := engine(cfg, dir, &s.snapshot, newSequentialSource(&s))
		got, err := baseline.ReplayContexts(context.Background(), s.Owner, s.Contexts, []discovery.Kind{discovery.Broadcast}, 20, s.At)
		close()
		must(err)
		before, _ := json.Marshal(s.Before)
		same, _ := json.Marshal(got)
		if string(before) != string(same) {
			panic("A/A baseline mismatch")
		}
		snapshots = append(snapshots, &s)
		comparisons = append(comparisons, refillComparison{Owner: s.Owner})
	}
	for _, enabled := range []bool{false, true} {
		sources := []*sequentialSource{}
		engines := []*discovery.Engine{}
		closers := []func(){}
		claims := map[int64]bool{}
		for _, s := range snapshots {
			for id, claimed := range s.Claims {
				if claimed {
					claims[id] = true
				}
			}
		}
		for _, s := range snapshots {
			copy := s.snapshot
			copy.Claims = claims
			source := newSequentialSource(s)
			e, close := engine(cfg, dir, &copy, source)
			if enabled {
				e.ConsumedRefillBPS = 10000
			}
			sources = append(sources, source)
			engines = append(engines, e)
			closers = append(closers, close)
		}
		// Standardized repeated refresh workload: 24 requests x 20 slots per Agent.
		// Corpus/context/clock are frozen; consumption and cross-Agent claims advance.
		for step := 0; step < 24; step++ {
			for i, s := range snapshots {
				prior := rejectionCounters()
				x, err := engines[i].ReplayPrefetchContexts(context.Background(), s.Owner, s.Contexts, []discovery.Kind{discovery.Broadcast}, 20, s.At)
				must(err)
				delivered, _ := discovery.SelectRecommendationPage(x.Candidates, 20, x.SourceLimits)
				for _, c := range delivered {
					must(sources[i].consume(c))
					if c.ClaimTTLSeconds > 0 {
						claims[c.Document.Ref.ID] = true
					}
				}
				arm := &comparisons[i].Before
				if enabled {
					arm = &comparisons[i].After
				}
				arm.PoolIncomplete = sources[i].poolIncomplete
				arm.PoolIncompleteReasons = nil
				for reason := range sources[i].poolReasons {
					arm.PoolIncompleteReasons = append(arm.PoolIncompleteReasons, reason)
				}
				sort.Strings(arm.PoolIncompleteReasons)
				if arm.Rejections == nil {
					arm.Rejections = map[string]float64{}
				}
				rejected := map[string]float64{}
				for reason, count := range rejectionCounters() {
					rejected[reason] = count - prior[reason]
					arm.Rejections[reason] += rejected[reason]
				}
				arm.Steps = append(arm.Steps, refillStep{Step: step + 1, Delivered: delivered, Refill: x.Refill, Status: x.Status, PartialReasons: x.PartialReasons, Rejections: rejected})
			}
		}
		for _, close := range closers {
			close()
		}
	}
	for _, c := range comparisons {
		save(filepath.Join(dir, fmt.Sprintf("refill-comparison-%d.json", c.Owner)), c)
	}
	inputs := map[string]string{}
	for _, name := range []string{"cohort.json", "rules.json", "model.json", "rerank.yaml", "rate-history.jsonl"} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		must(err)
		inputs[name] = fmt.Sprintf("%x", sha256.Sum256(raw))
	}
	save(filepath.Join(dir, "refill-manifest.json"), map[string]any{"snapshots_sha256": fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(snapshotHashes, "\n")))), "input_sha256": inputs, "capture_format": refillCaptureFormat, "agents": 225, "steps": 24, "page_size": 20, "capture_depth": refillCaptureDepth, "schedule": "repeated refresh; frozen clock/corpus/contexts; advancing consumption and shared injection claims", "aa_equal": 225})
	fmt.Println("225 Agents: A/A exact; 24 sequential refresh requests in each arm; no duplicate/initially consumed deliveries")
}
