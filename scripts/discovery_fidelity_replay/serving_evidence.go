package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"

	"eigenflux_server/pkg/config"
	"eigenflux_server/rpc/sort/discovery"
	"eigenflux_server/rpc/sort/discoverylr"
	"path/filepath"
	"time"
)

// Serving evidence includes optional production branches. A missing fact is an
// invalid capture, never an invitation to replay a different serving fallback.
const servingCaptureFormat = "current-serving-v1"

type servingEvidence struct {
	Format                                                            string
	NeedBudgetBPS, ProfileSemanticBPS, RatingLRBPS, ConsumedRefillBPS int
	FriendFeedEnabled                                                 bool
	Profile                                                           []float32
	ProfileRead                                                       bool
	Cosines                                                           map[string]map[int64]float64
	CosineRead                                                        map[string]map[int64]bool
	Groups                                                            []int64
	GroupsRead                                                        bool
	GroupsComplete                                                    bool
}

type servingSource struct {
	captureContext context.Context
	discovery.Sources
	snap     *snapshot
	live     *discovery.Source
	recorder *recordedSource
	mu       *sync.Mutex
}

func vectorKey(v []float32) string {
	b, err := json.Marshal(v)
	must(err)
	return fmt.Sprintf("%x", sha256.Sum256(b))
}
func evidenceKey(kind string, v any) string {
	b, err := json.Marshal(v)
	must(err)
	return kind + ":" + fmt.Sprintf("%x", sha256.Sum256(b))
}
func (s *servingSource) fail(err error) error {
	if err != nil {
		s.mu.Lock()
		s.snap.CaptureError = "serving evidence: " + err.Error()
		s.mu.Unlock()
	}
	return err
}
func (s *servingSource) ProfileVector(ctx context.Context, owner int64) ([]float32, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.snap.Serving
	if e == nil || owner != s.snap.Owner {
		s.snap.CaptureError = "missing serving profile evidence"
		return nil, fmt.Errorf("missing serving profile evidence")
	}
	if e.ProfileRead {
		return e.Profile, nil
	}
	if s.live == nil {
		s.snap.CaptureError = "uncaptured profile"
		return nil, fmt.Errorf("uncaptured profile")
	}
	work, cancel := s.captureWork()
	defer cancel()
	v, err := s.live.ProfileVector(work, owner)
	if err != nil {
		s.snap.CaptureError = "profile evidence unavailable"
		return nil, err
	}
	e.Profile = v
	e.ProfileRead = true
	return v, nil
}
func (s *servingSource) Cosine(ctx context.Context, v []float32, ids []int64) (map[int64]float64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.snap.Serving
	if e == nil {
		s.snap.CaptureError = "missing serving cosine evidence"
		return nil, fmt.Errorf("missing serving cosine evidence")
	}
	key := vectorKey(v)
	if e.Cosines == nil {
		e.Cosines = map[string]map[int64]float64{}
		e.CosineRead = map[string]map[int64]bool{}
	}
	if e.Cosines[key] == nil {
		e.Cosines[key] = map[int64]float64{}
		e.CosineRead[key] = map[int64]bool{}
	}
	missing := []int64{}
	for _, id := range ids {
		if !e.CosineRead[key][id] {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		if s.live == nil {
			s.snap.CaptureError = "uncaptured cosine items"
			return nil, fmt.Errorf("uncaptured cosine items")
		}
		work, cancel := s.captureWork()
		defer cancel()
		values, err := s.live.Cosine(work, v, missing)
		if err != nil {
			s.snap.CaptureError = "cosine evidence unavailable"
			return nil, err
		}
		for _, id := range missing {
			e.CosineRead[key][id] = true
			if x, ok := values[id]; ok {
				e.Cosines[key][id] = x
			}
		}
	}
	out := map[int64]float64{}
	for _, id := range ids {
		if x, ok := e.Cosines[key][id]; ok {
			out[id] = x
		}
	}
	return out, nil
}
func (s *servingSource) ConsumedGroups(ctx context.Context, owner int64) ([]int64, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.snap.Serving
	if e == nil || owner != s.snap.Owner {
		s.snap.CaptureError = "missing consumption evidence"
		return nil, false, fmt.Errorf("missing consumption evidence")
	}
	if e.GroupsRead {
		return e.Groups, e.GroupsComplete, nil
	}
	if s.live == nil {
		s.snap.CaptureError = "uncaptured consumed groups"
		return nil, false, fmt.Errorf("uncaptured consumed groups")
	}
	work, cancel := s.captureWork()
	defer cancel()
	var groups []int64
	var complete bool
	var err error
	if captureSlim != nil {
		var r slimConsumption
		r, err = captureSlim.consumption(work, owner, "history", nil)
		groups, complete = r.Confirmed, r.Complete
		if err == nil && r.Unavailable {
			err = fmt.Errorf("history capture exceeded bound")
		}
	} else {
		groups, complete, err = s.live.ConsumedGroups(work, owner)
	}
	if err != nil {
		s.snap.CaptureError = "consumption evidence unavailable"
		return nil, false, err
	}
	e.Groups = groups
	e.GroupsComplete = complete
	e.GroupsRead = true
	return groups, complete, nil
}
func (s *servingSource) recall(ctx context.Context, c discovery.Context, key string, limit int, fetch func() ([]discovery.Document, error)) ([]discovery.Document, error) {
	ck := channelKey(c, discovery.Broadcast, key)
	s.mu.Lock()
	call, ok := s.snap.Channels[ck]
	if !ok && strings.HasPrefix(key, "profile:") {
		// Profile kNN is shared across clauses. Goroutine completion must not
		// decide whether an otherwise identical frozen query can be replayed.
		for _, candidate := range s.snap.Channels {
			if candidate.Name != key {
				continue
			}
			for _, prior := range s.snap.Contexts {
				if prior.ID == candidate.ContextID && reflect.DeepEqual(prior.Filters, c.Filters) {
					call = candidate
					ok = true
					break
				}
			}
			if ok {
				break
			}
		}
	}
	s.mu.Unlock()
	if ok {
		if call.Limit != limit || call.Error != "" {
			return nil, fmt.Errorf("incompatible serving recall")
		}
		return restoreDocs(call.Documents), nil
	}
	if s.live == nil {
		return nil, s.fail(fmt.Errorf("uncaptured serving recall"))
	}
	docs, err := fetch()
	call = channel{ContextID: c.ID, Kind: discovery.Broadcast, Name: key, Limit: limit, Documents: storeDocs(docs)}
	if err != nil {
		call.Error = err.Error()
	}
	// Ordinary and semantic recall share one recorder lock and one union.
	s.mu.Lock()
	s.snap.Channels[ck] = call
	s.recorder.prepared = false
	s.mu.Unlock()
	return docs, s.fail(err)
}
func (s *servingSource) RecallProfile(ctx context.Context, c discovery.Context, v []float32, since int64, limit int) ([]discovery.Document, error) {
	key := evidenceKey("profile", struct {
		Vector []float32
		Since  int64
	}{v, since})
	return s.recall(ctx, c, key, limit, func() ([]discovery.Document, error) {
		if captureSlim != nil {
			c.Vector = v
			q, err := discovery.ReplayChannelQuery(c, "dense", limit, s.snap.Serving.Groups, nil, s.snap.At)
			if err != nil {
				return nil, err
			}
			if since > 0 {
				b := q["knn"].(map[string]any)["filter"].(map[string]any)["bool"].(map[string]any)
				b["filter"] = append(b["filter"].([]any), map[string]any{"range": map[string]any{"created_at": map[string]any{"gte": time.UnixMilli(since).UTC().Format(time.RFC3339Nano)}}})
			}
			work, cancel := s.captureWork()
			defer cancel()
			return captureSlim.RawQuery(work, q, "dense", true)
		}
		return s.live.RecallProfile(ctx, c, v, since, limit)
	})
}
func (s *servingSource) RecallUnseen(ctx context.Context, c discovery.Context, groups, ids []int64, limit int) ([]discovery.Document, error) {
	key := evidenceKey("unseen", struct{ Groups, IDs []int64 }{groups, ids})
	return s.recall(ctx, c, key, limit, func() ([]discovery.Document, error) {
		work, cancel := s.captureWork()
		defer cancel()
		if captureSlim != nil {
			q, err := discovery.ReplayChannelQuery(c, "lexical", limit, groups, ids, s.snap.At)
			if err != nil {
				return nil, err
			}
			return captureSlim.RawQuery(work, q, "lexical", true)
		}
		return s.live.RecallUnseen(work, c, groups, ids, limit)
	})
}

func captureServing(ctx context.Context, cfg *config.Config, s *snapshot, r *recordedSource) *servingSource {
	if cfg.DiscoveryRatingLRV2BPS != 0 {
		panic("rating challenger capture is not supported")
	}
	s.Serving = &servingEvidence{Format: servingCaptureFormat, NeedBudgetBPS: cfg.DiscoveryNeedBudgetBPS, ProfileSemanticBPS: cfg.DiscoveryProfileSemanticBPS, RatingLRBPS: cfg.DiscoveryRatingLRBPS, ConsumedRefillBPS: cfg.DiscoveryConsumedRefillBPS, FriendFeedEnabled: cfg.FriendFeedEnabled}
	return &servingSource{captureContext: ctx, Sources: r, snap: s, live: r.live, recorder: r, mu: &r.mu}
}
func frozenServing(s *snapshot, source discovery.Sources) discovery.Sources {
	if s.Serving == nil {
		return source
	}
	return &servingSource{Sources: source, snap: s, mu: &sync.Mutex{}}
}
func applyServing(e *discovery.Engine, dir string, s *snapshot) func() {
	if s.Serving == nil {
		return func() {}
	}
	v := s.Serving
	e.NeedBudgetBPS = v.NeedBudgetBPS
	e.ProfileSemanticBPS = v.ProfileSemanticBPS
	e.RatingLRBPS = v.RatingLRBPS
	e.ConsumedRefillBPS = v.ConsumedRefillBPS
	e.FriendFeedEnabled = v.FriendFeedEnabled
	rating := discoverylr.NewRating(v.RatingLRBPS > 0, filepath.Join(dir, "rating-model.json"), 24*time.Hour)
	if v.RatingLRBPS > 0 {
		if _, ok := rating.ScoreBatch(nil); !ok {
			rating.Close()
			panic("frozen rating model unavailable")
		}
	}
	e.RatingLearned = rating
	e.PGCAuthors = func(_ context.Context, ids []int64) (map[int64]bool, error) {
		classes := map[int64]bool{}
		for _, d := range s.Inventory {
			class, ok := s.Classes[d.Ref.ID]
			if !ok {
				continue
			}
			if class != "pgc" && class != "ugc" {
				return nil, fmt.Errorf("unknown source class")
			}
			classes[d.AuthorID] = class == "pgc"
		}
		out := map[int64]bool{}
		for _, id := range ids {
			v, ok := classes[id]
			if !ok {
				return nil, fmt.Errorf("missing author class")
			}
			out[id] = v
		}
		return out, nil
	}
	return rating.Close
}

// Capture observes healthy-dependency facts, not network latency through an SSH
// tunnel. Only capture reads get this budget; frozen execution retains serving
// deadlines. The overall capture context still bounds work and cancellation.
func (s *servingSource) captureWork() (context.Context, context.CancelFunc) {
	return context.WithTimeout(s.captureContext, 30*time.Second)
}

func (s *servingSource) Hydrate(ctx context.Context, owner int64, mode discovery.Mode, docs []discovery.Document) ([]discovery.Document, error) {
	if s.live != nil {
		// Preparing the whole union performs multiple bounded store reads. Keep
		// the ten-minute Agent deadline rather than applying one read's budget
		// to the complete preparation; individual transports remain bounded.
		return s.Sources.Hydrate(s.captureContext, owner, mode, docs)
	}
	return s.Sources.Hydrate(ctx, owner, mode, docs)
}
