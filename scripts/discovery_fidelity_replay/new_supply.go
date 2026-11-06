package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"

	"eigenflux_server/pkg/featureindex"
	"eigenflux_server/rpc/sort/discovery"
)

// Each owner fact is required even when false: unseen/visible are observations,
// not defaults. Grouping must be computed against retained production items.
type supplyOwner struct{ Seen, Blocked, KnownContact bool }
type supplyItem struct {
	Document   storedDocument
	Evidence   itemEvidence
	Vector     []float32
	Owners     map[int64]supplyOwner
	Provenance string
}

func loadNewSupply(dir string, snapshots []*snapshot) ([]supplyItem, error) {
	b, err := os.ReadFile(filepath.Join(dir, "new-supply.json"))
	if err != nil {
		return nil, err
	}
	var items []supplyItem
	if err = json.Unmarshal(b, &items); err != nil {
		return nil, err
	}
	if len(items) == 0 || len(items) > 50 {
		return nil, fmt.Errorf("new supply requires 1-50 bounded candidates")
	}
	occupied := map[int64]bool{}
	for _, s := range snapshots {
		if s.Serving == nil || s.Serving.Format != servingCaptureFormat {
			return nil, fmt.Errorf("new supply requires current serving evidence")
		}
		for _, d := range s.Inventory {
			occupied[d.Ref.ID] = true
		}
	}
	for _, x := range items {
		d := restoreDocs([]storedDocument{x.Document})[0]
		if d.Ref.Type != discovery.Broadcast || d.Ref.ID <= 0 || occupied[d.Ref.ID] || d.AuthorID <= 0 || d.GroupID <= 0 || x.Provenance == "" {
			return nil, fmt.Errorf("invalid or colliding new-supply identity/provenance")
		}
		occupied[d.Ref.ID] = true
		if x.Evidence.ItemID != d.Ref.ID || x.Evidence.AuthorID != d.AuthorID || d.Text != x.Evidence.Content+"\n"+x.Evidence.Summary || d.Version != featureindex.BroadcastContentHash(d.Ref.ID, d.AuthorID, d.Text, d.Slots) {
			return nil, fmt.Errorf("new supply content fingerprint mismatch")
		}
		if !d.Active || d.FreshAt <= 0 || d.Quality < 0 || d.Quality > 1 || math.IsNaN(d.Quality) || len(x.Vector) == 0 {
			return nil, fmt.Errorf("invalid new supply lifecycle/quality/vector")
		}
		if _, err := cosineScore(x.Vector, x.Vector); err != nil {
			return nil, err
		}
		for _, v := range x.Vector {
			if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
				return nil, fmt.Errorf("nonfinite supply vector")
			}
		}
		for _, s := range snapshots {
			if _, ok := x.Owners[s.Owner]; !ok {
				return nil, fmt.Errorf("missing new supply owner authority")
			}
			if d.FreshAt > s.At {
				return nil, fmt.Errorf("new supply created after frozen request")
			}
		}
	}
	return items, nil
}

func supplySnapshot(s *snapshot, items []supplyItem) *snapshot {
	copy := *s
	copy.Inventory = append([]storedDocument(nil), s.Inventory...)
	copy.Classes = map[int64]string{}
	for id, v := range s.Classes {
		copy.Classes[id] = v
	}
	copy.Seen = map[string]bool{}
	for id, v := range s.Seen {
		copy.Seen[id] = v
	}
	for _, x := range items {
		d := x.Document
		f := x.Owners[s.Owner]
		d.Blocked = f.Blocked
		d.KnownContact = f.KnownContact
		copy.Inventory = append(copy.Inventory, d)
		copy.Classes[d.Ref.ID] = "pgc"
		copy.Seen[d.Ref.Key()] = f.Seen
	}
	return &copy
}

// The pool handles lexical and ordinary dense competition. The wrapper also
// admits new embeddings to current profile recall and rating evidence.
type supplySource struct {
	*poolSource
	serving            *servingSource
	additions          map[int64][]float32
	refreshedNewRecall bool
}

func (s *supplySource) ProfileVector(ctx context.Context, owner int64) ([]float32, error) {
	return s.serving.ProfileVector(ctx, owner)
}
func (s *supplySource) Cosine(ctx context.Context, v []float32, ids []int64) (map[int64]float64, error) {
	old := []int64{}
	out := map[int64]float64{}
	for _, id := range ids {
		if extra, ok := s.additions[id]; ok {
			x, err := cosineScore(v, extra)
			if err != nil {
				return nil, err
			}
			out[id] = 2*x - 1
		} else {
			old = append(old, id)
		}
	}
	prior, err := s.serving.Cosine(ctx, v, old)
	if err != nil {
		return nil, err
	}
	for id, x := range prior {
		out[id] = x
	}
	return out, nil
}
func (s *supplySource) ConsumedGroups(ctx context.Context, owner int64) ([]int64, bool, error) {
	return s.serving.ConsumedGroups(ctx, owner)
}
func (s *supplySource) RecallProfile(ctx context.Context, c discovery.Context, v []float32, since int64, limit int) ([]discovery.Document, error) {
	docs, err := s.serving.RecallProfile(ctx, c, v, since, limit)
	if err != nil {
		return nil, err
	}
	// Source.RecallProfile excludes confirmed consumed groups before its cutoff.
	groups, _, err := s.ConsumedGroups(ctx, c.OwnerID)
	if err != nil {
		return nil, err
	}
	excluded := map[int64]bool{}
	for _, g := range groups {
		excluded[g] = true
	}
	eligible := map[int64][]float32{}
	for id, vec := range s.additions {
		d := s.docs[id]
		if !excluded[d.GroupID] && (since <= 0 || d.FreshAt >= since) && (d.ExpiresAt == 0 || d.ExpiresAt > s.snap.At) {
			eligible[id] = vec
		}
	}
	p := *s.poolSource
	p.vectors = eligible
	c.Vector = v
	return p.dense(c, docs, limit)
}
func (s *supplySource) RecallUnseen(ctx context.Context, c discovery.Context, groups, ids []int64, limit int) ([]discovery.Document, error) {
	docs, err := s.serving.RecallUnseen(ctx, c, groups, ids, limit)
	if err != nil {
		return nil, err
	}
	excluded := map[int64]bool{}
	for _, g := range groups {
		excluded[g] = true
	}
	excludedIDs := map[int64]bool{}
	for _, id := range ids {
		excludedIDs[id] = true
	}
	changed := map[int64]bool{}
	for id := range s.additions {
		d := s.docs[id]
		if !excluded[d.GroupID] && !excludedIDs[id] {
			changed[id] = true
		}
	}
	copy := *s.snap
	copy.Channels = map[string]channel{}
	for key, call := range s.snap.Channels {
		copy.Channels[key] = call
	}
	copy.Channels[channelKey(c, discovery.Broadcast, "lexical")] = channel{ContextID: c.ID, Kind: discovery.Broadcast, Name: "lexical", Limit: limit, Documents: storeDocs(docs)}
	p := *s.poolSource
	p.snap = &copy
	p.changed = changed
	return p.calibratedLexical(ctx, c, discovery.Broadcast, limit)
}

func (s *supplySource) Recall(ctx context.Context, c discovery.Context, k discovery.Kind, ch string, limit int) ([]discovery.Document, error) {
	if ch == "new_recall" && s.refreshedNewRecall {
		prior, err := s.poolSource.Recall(ctx, c, k, ch, limit)
		if err != nil {
			return nil, err
		}
		// Sensitivity: the next offline job admits each new item to its top-k.
		// Membership is explicit; this is not a reconstructed hourly job schedule.
		ids := make([]int64, 0, len(s.additions))
		for id := range s.additions {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		out := []discovery.Document{}
		for _, id := range ids {
			out = append(out, discovery.Document{Ref: s.docs[id].Ref, NeedExclusionText: len(c.Filters.ExcludeTerms) > 0})
		}
		out = append(out, prior...)
		if len(out) > limit {
			out = out[:limit]
		}
		return out, nil
	}
	if ch != "dense" {
		return s.poolSource.Recall(ctx, c, k, ch, limit)
	}
	query, err := discovery.Query(c, k, ch, limit)
	if err != nil {
		return nil, err
	}
	excluded := map[int64]bool{}
	knn, ok := query["knn"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("missing dense recall query")
	}
	filter := knn["filter"].(map[string]any)["bool"].(map[string]any)
	not, _ := filter["must_not"].([]any)
	for _, clause := range not {
		m, ok := clause.(map[string]any)
		if !ok {
			continue
		}
		terms, ok := m["terms"].(map[string]any)
		if !ok {
			continue
		}
		if groups, ok := terms["group_id"].([]int64); ok {
			for _, g := range groups {
				excluded[g] = true
			}
		}
	}
	p := *s.poolSource
	p.vectors = map[int64][]float32{}
	for id, v := range s.vectors {
		d := s.docs[id]
		if !excluded[d.GroupID] && (d.ExpiresAt == 0 || d.ExpiresAt > s.snap.At) {
			p.vectors[id] = v
		}
	}
	return p.Recall(ctx, c, k, ch, limit)
}
