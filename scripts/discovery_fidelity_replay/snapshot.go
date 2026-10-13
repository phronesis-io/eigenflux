package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"eigenflux_server/rpc/sort/discovery"
)

type channel struct {
	ContextID int64
	Kind      discovery.Kind
	Name      string
	Limit     int
	Documents []storedDocument
	Error     string
}

// Private replay evidence must retain exclusion text, which production API JSON
// deliberately omits. These snapshots are never committed or served by the API.
type storedDocument struct {
	discovery.Document
	ExclusionText  string
	NeedsExclusion bool
}

func storeDocs(docs []discovery.Document) []storedDocument {
	out := make([]storedDocument, len(docs))
	for i, d := range docs {
		out[i] = storedDocument{d, d.Text, d.NeedExclusionText}
	}
	return out
}
func restoreDocs(docs []storedDocument) []discovery.Document {
	out := make([]discovery.Document, len(docs))
	for i, d := range docs {
		out[i] = d.Document
		out[i].Text = d.ExclusionText
		out[i].NeedExclusionText = d.NeedsExclusion
	}
	return out
}

type hydrationBatch struct {
	Input  []storedDocument
	Output []storedDocument
}
type snapshot struct {
	Inventory    []storedDocument
	Hydration    []hydrationBatch
	Owner        int64
	At           int64
	Contexts     []discovery.Context
	Vectors      map[int64][]float32
	Channels     map[string]channel
	Hydrated     map[string]discovery.Document
	Seen         map[string]bool
	Classes      map[int64]string
	Claims       map[int64]bool
	Before       discovery.Execution
	CaptureError string
}

func channelKey(c discovery.Context, k discovery.Kind, ch string) string {
	return fmt.Sprintf("%d:%s:%s", c.ID, k, ch)
}

// Every recall channel, including empty/error results, is retained before merge,
// authority hydration, deduplication and relevance thresholds.
type recordedSource struct {
	pools     map[string][]discovery.Document
	prepared  bool
	inventory map[string]discovery.Document
	live      *discovery.Source
	hydrate   *discovery.Source
	snap      *snapshot
	mu        sync.Mutex
	facts     func(context.Context, []discovery.Document) error
}

func (s *recordedSource) Owner(ctx context.Context, id int64) (discovery.OwnerContext, error) {
	return s.live.Owner(ctx, id)
}
func (s *recordedSource) Recall(ctx context.Context, c discovery.Context, k discovery.Kind, ch string, limit int) ([]discovery.Document, error) {
	var docs []discovery.Document
	var err error
	if pool, ok := s.pools[ch]; ok {
		docs = append([]discovery.Document(nil), pool...)
		for i := range docs {
			docs[i].NeedExclusionText = len(c.Filters.ExcludeTerms) > 0
		}
	} else {
		docs, err = s.live.Recall(ctx, c, k, ch, limit)
	}
	call := channel{c.ID, k, ch, limit, storeDocs(docs), ""}
	if err != nil {
		call.Error = err.Error()
	}
	s.mu.Lock()
	s.snap.Channels[channelKey(c, k, ch)] = call
	s.mu.Unlock()
	return docs, err
}
func (s *recordedSource) Hydrate(ctx context.Context, owner int64, mode discovery.Mode, docs []discovery.Document) ([]discovery.Document, error) {
	if !s.prepared {
		if err := s.prepare(ctx, owner, mode); err != nil {
			return nil, err
		}
	}
	fresh := []discovery.Document{}
	for _, d := range docs {
		if v, ok := s.inventory[d.Ref.Key()]; ok {
			fresh = append(fresh, v)
		}
	}
	s.snap.Hydration = append(s.snap.Hydration, hydrationBatch{storeDocs(docs), storeDocs(fresh)})
	return fresh, nil
}
func (s *recordedSource) Seen(ctx context.Context, owner int64, docs []discovery.Document) (map[string]bool, error) {
	return (&frozenSource{snap: s.snap}).Seen(ctx, owner, docs)
}

// All recall completes before Engine asks for hydration. Resolve the entire
// union once at a common authority snapshot; per-context ranking stays unchanged.
func (s *recordedSource) prepare(ctx context.Context, owner int64, mode discovery.Mode) error {
	union := map[string]discovery.Document{}
	for _, ch := range s.snap.Channels {
		for _, stored := range ch.Documents {
			d := restoreDocs([]storedDocument{stored})[0]
			d.NeedExclusionText = true
			if prior, ok := union[d.Ref.Key()]; !ok || prior.Version == "" && d.Version != "" {
				union[d.Ref.Key()] = d
			}
		}
	}
	docs := make([]discovery.Document, 0, len(union))
	for _, d := range union {
		docs = append(docs, d)
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].Ref.ID < docs[j].Ref.ID })
	s.inventory = map[string]discovery.Document{}
	for i := 0; i < len(docs); i += 500 {
		end := i + 500
		if end > len(docs) {
			end = len(docs)
		}
		fresh, err := s.hydrate.Hydrate(ctx, owner, mode, docs[i:end])
		if err != nil {
			return err
		}
		// Recall source text already has a content fingerprint. Reuse it only
		// after the authoritative hydration confirms the same version.
		missing := []discovery.Document{}
		for j := range fresh {
			prior := union[fresh[j].Ref.Key()]
			if prior.Version != "" && prior.Version == fresh[j].Version {
				fresh[j].Text = prior.Text
				fresh[j].Preview = prior.Preview
			} else if prior.Version != "" {
				prior.Version = ""
				missing = append(missing, prior)
			}
		}
		if len(missing) > 0 {
			repaired, e := s.hydrate.Hydrate(ctx, owner, mode, missing)
			if e != nil {
				return e
			}
			byID := map[int64]discovery.Document{}
			for _, d := range repaired {
				byID[d.Ref.ID] = d
			}
			for j := range fresh {
				if d, ok := byID[fresh[j].Ref.ID]; ok {
					fresh[j] = d
				}
			}
		}
		if s.facts != nil {
			if err = s.facts(ctx, fresh); err != nil {
				return err
			}
		}
		seen, err := s.live.Seen(ctx, owner, fresh)
		if err != nil {
			return err
		}
		for _, d := range fresh {
			s.inventory[d.Ref.Key()] = d
			s.snap.Hydrated[d.Ref.Key()] = d
			s.snap.Seen[d.Ref.Key()] = seen[d.Ref.Key()]
		}
		s.snap.Inventory = append(s.snap.Inventory, storeDocs(fresh)...)
	}
	s.prepared = true
	return nil
}

type frozenSource struct {
	snap *snapshot
	next int
}

func newFrozenSource(s *snapshot) *frozenSource { return &frozenSource{snap: s} }

func (s *frozenSource) Owner(context.Context, int64) (discovery.OwnerContext, error) {
	return discovery.OwnerContext{}, fmt.Errorf("owner read outside frozen context")
}
func (s *frozenSource) Recall(_ context.Context, c discovery.Context, k discovery.Kind, ch string, limit int) ([]discovery.Document, error) {
	call, ok := s.snap.Channels[channelKey(c, k, ch)]
	if !ok || call.Limit != limit {
		return nil, fmt.Errorf("missing recall snapshot %s", channelKey(c, k, ch))
	}
	if call.Error != "" {
		return nil, fmt.Errorf("captured channel error: %s", call.Error)
	}
	return restoreDocs(call.Documents), nil
}
func (s *frozenSource) Hydrate(_ context.Context, _ int64, _ discovery.Mode, docs []discovery.Document) ([]discovery.Document, error) {
	if s.next >= len(s.snap.Hydration) {
		return nil, fmt.Errorf("uncaptured hydration batch")
	}
	batch := s.snap.Hydration[s.next]
	s.next++
	a, _ := json.Marshal(storeDocs(docs))
	b, _ := json.Marshal(batch.Input)
	if string(a) != string(b) {
		return nil, fmt.Errorf("hydration input differs from captured pool")
	}
	return restoreDocs(batch.Output), nil
}
func (s *frozenSource) Seen(_ context.Context, _ int64, docs []discovery.Document) (map[string]bool, error) {
	out := map[string]bool{}
	for _, d := range docs {
		v, ok := s.snap.Seen[d.Ref.Key()]
		if !ok {
			return nil, fmt.Errorf("missing seen snapshot")
		}
		out[d.Ref.Key()] = v
	}
	return out, nil
}
