package discovery

import (
	"context"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"eigenflux_server/pkg/metrics"
	"eigenflux_server/rpc/sort/discoverylr"
)

const (
	ratingLRSalt = "discovery-rating-lr-v1:"
	// ratingEvidenceTimeout bounds the parallel profile, Need and author reads
	// of one selection; a refill reads only its new evidence.
	ratingEvidenceTimeout = 300 * time.Millisecond
	// cosineTolerance admits rounding of ES similarity scores at ±1.
	cosineTolerance = 1e-6
	ratingMaxNeeds  = 16
)

var (
	ratingContentTypes = []string{"info", "demand", "supply", "alert"}
	ratingChannels     = []string{"dense", "friend", "hot_recall", "lexical", "lexical_recent", "new_recall", "new_ugc_recall", "swing_i2i"}
	ratingSourceTypes  = []string{"curated", "original", "forwarded"}
)

// PGCAuthors reports, for each requested author ID, whether the author is a
// PGC publisher. An error leaves the request on its existing ranking.
type PGCAuthors func(context.Context, []int64) (map[int64]bool, error)

// ratingSimilarity is one candidate's similarity evidence. Needs counts the
// Agent's distinct captured Need vectors with a cosine for the item; an item
// without an embedding has none.
type ratingSimilarity struct {
	OwnNeed    float64 `json:"own_need"`
	MaxNeed    float64 `json:"max_need"`
	Needs      int     `json:"needs"`
	Profile    float64 `json:"profile"`
	HasProfile bool    `json:"has_profile"`
}

// ratingRequest caches one request's evidence across the initial selection
// and a refill. After any failed read the request keeps the existing ranking.
type ratingRequest struct {
	failed  bool
	started bool
	// profileRead is set once the profile vector is known; an Agent without
	// one has no profile similarity.
	profileRead bool
	profile     []float32
	needs       [][]float32
	needOf      map[int64]int
	profileCos  map[int64]float64
	needCos     []map[int64]float64
	read        map[int64]bool
	pgc         map[int64]bool
}

func ratingEnabled(owner int64, bps int) bool { return bucketEnabled(ratingLRSalt, owner, bps) }

func ratingRanked(c Candidate) bool {
	return c.LR != nil && c.LR.Contract == discoverylr.RatingContract
}

func injected(c Candidate) bool {
	return slices.ContainsFunc(c.Reasons, func(r string) bool { return strings.HasPrefix(r, "inject:") })
}

// ratingVector builds the discovery_rating_lr_v1 input. channels is the
// item's recall attribution across the request's candidates, which is what
// Merge records for delivery.
func ratingVector(c Candidate, sim ratingSimilarity, channels []string, pgc bool) ([]float64, bool) {
	v, ok := learningVector(c)
	if !ok {
		return nil, false
	}
	flag := func(b bool) {
		x := 0.0
		if b {
			x = 1
		}
		v = append(v, x)
	}
	clip := func(x float64) float64 { return math.Max(0, math.Min(1, x)) }
	d := c.Document
	flag(c.Context.Origin == "need_input")
	flag(c.Context.Origin == "agent_context")
	for _, t := range ratingContentTypes {
		flag(d.ContentType == t)
	}
	flag(pgc)
	flag(d.KnownContact)
	for _, ch := range ratingChannels {
		flag(slices.Contains(channels, ch))
	}
	for _, t := range ratingSourceTypes {
		flag(d.SourceType == t)
	}
	ttl := 0.0
	if d.ExpiresAt > 0 {
		ttl = math.Max(0, math.Min(30, float64(d.ExpiresAt-c.Score.RequestTime)/float64(day))) / 30
	}
	v = append(v, ttl)
	for _, cos := range []float64{sim.OwnNeed, sim.MaxNeed, sim.Profile} {
		if !finite(cos) || math.Abs(cos) > 1+cosineTolerance {
			return nil, false
		}
	}
	need, profile := 0.0, 0.0
	if sim.Needs > 0 {
		need = sim.MaxNeed
	}
	if sim.HasProfile {
		profile = sim.Profile
	}
	v = append(v, clip(sim.OwnNeed), clip(need), clip(profile), math.Max(clip(need), clip(profile)))
	flag(sim.Needs > 0)
	flag(sim.HasProfile)
	v = append(v, clip(need-.35), clip(need-.45), clip(profile-.35), clip(profile-.45), clip(float64(sim.Needs)/10))
	return v, len(v) == len(discoverylr.RatingFeatureNames)
}

func vectorKey(v []float32) uint64 {
	h := fnv.New64a()
	var b [4]byte
	for _, x := range v {
		binary.BigEndian.PutUint32(b[:], math.Float32bits(x))
		_, _ = h.Write(b[:])
	}
	return h.Sum64()
}

// start collects the Agent's distinct captured broadcast Need vectors. A
// pending Need vector has no similarity evidence.
func (s *ratingRequest) start(contexts []Context, profile []float32) error {
	s.started = true
	s.profile, s.profileRead = profile, len(profile) > 0
	s.needOf, s.read, s.pgc = map[int64]int{}, map[int64]bool{}, map[int64]bool{}
	seen := map[uint64]int{}
	for _, c := range contexts {
		if c.CapturedNeed == nil || c.UnverifiedNeedReason != "" || len(c.Vector) == 0 || !hasKind(c.Kinds, Broadcast) {
			continue
		}
		key := vectorKey(c.Vector)
		i, ok := seen[key]
		if !ok {
			i = len(s.needs)
			seen[key] = i
			s.needs = append(s.needs, c.Vector)
			s.needCos = append(s.needCos, map[int64]float64{})
		}
		s.needOf[c.ID] = i
	}
	if len(s.needs) > ratingMaxNeeds {
		return fmt.Errorf("need_budget")
	}
	return nil
}

// fetch reads similarity for unread items and classification for unread
// authors, all in parallel under the request's evidence deadline.
func (s *ratingRequest) fetch(ctx context.Context, src ProfileSemanticSources, pgc PGCAuthors, owner int64, items, authors []int64) error {
	ids := []int64{}
	for _, id := range items {
		if !s.read[id] {
			ids = append(ids, id)
		}
	}
	missingAuthors := []int64{}
	for _, id := range authors {
		if _, ok := s.pgc[id]; !ok {
			missingAuthors = append(missingAuthors, id)
		}
	}
	if len(ids) > maxCosineIDs {
		return fmt.Errorf("candidate_budget")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	var mu sync.Mutex
	var first error
	run := func(f func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := f(); err != nil {
				mu.Lock()
				if first == nil {
					first = err
					cancel()
				}
				mu.Unlock()
			}
		}()
	}
	var profileCos map[int64]float64
	needCos := make([]map[int64]float64, len(s.needs))
	var classes map[int64]bool
	if len(missingAuthors) > 0 {
		run(func() (err error) { classes, err = pgc(ctx, missingAuthors); return err })
	}
	profile, profileRead := s.profile, s.profileRead
	if len(ids) > 0 {
		run(func() (err error) {
			if !profileRead {
				if profile, err = src.ProfileVector(ctx, owner); err != nil {
					return err
				}
				profileRead = true
			}
			if len(profile) == 0 {
				return nil
			}
			profileCos, err = src.Cosine(ctx, profile, ids)
			return err
		})
		for i, vector := range s.needs {
			run(func() (err error) { needCos[i], err = src.Cosine(ctx, vector, ids); return err })
		}
	}
	wg.Wait()
	if first != nil {
		return first
	}
	for _, id := range missingAuthors {
		isPGC, ok := classes[id]
		if !ok {
			return fmt.Errorf("author_unclassified")
		}
		s.pgc[id] = isPGC
	}
	s.profile, s.profileRead = profile, profileRead
	if s.profileCos == nil {
		s.profileCos = map[int64]float64{}
	}
	for id, cos := range profileCos {
		s.profileCos[id] = cos
	}
	for i, got := range needCos {
		for id, cos := range got {
			s.needCos[i][id] = cos
		}
	}
	for _, id := range ids {
		s.read[id] = true
	}
	return nil
}

func (s *ratingRequest) similarity(c Candidate) ratingSimilarity {
	id := c.Document.Ref.ID
	sim := ratingSimilarity{}
	sim.Profile, sim.HasProfile = s.profileCos[id]
	own, ownNeed := s.needOf[c.Context.ID]
	for i, table := range s.needCos {
		cos, ok := table[id]
		if !ok {
			continue
		}
		if sim.Needs == 0 || cos > sim.MaxNeed {
			sim.MaxNeed = cos
		}
		sim.Needs++
		if ownNeed && i == own {
			sim.OwnNeed = cos
		}
	}
	return sim
}

// rankRating scores every eligible candidate of a broadcast-only
// recommendation (the Feed request the model was trained on) with the
// positive-rating model for Agents in its bucket. It returns false, leaving
// candidates untouched, when the Agent is not selected or any evidence, input
// or score is unavailable, so one request never mixes rating probabilities
// with another scorer.
func (e *Engine) rankRating(ctx context.Context, owner int64, kinds []Kind, contexts []Context, candidates []Candidate, mode Mode, profile []float32, state *ratingRequest) bool {
	if mode != Recommendation || len(kinds) != 1 || kinds[0] != Broadcast || e.RatingLearned == nil || e.PGCAuthors == nil || state.failed || !ratingEnabled(owner, e.RatingLRBPS) {
		return false
	}
	src, ok := e.Sources.(ProfileSemanticSources)
	if !ok {
		return false
	}
	fallback := func(reason string) bool {
		state.failed = true
		metrics.DiscoveryLRScoring.WithLabelValues("rating_" + reason).Inc()
		return false
	}
	indices, items, authors := []int{}, []int64{}, []int64{}
	channels := map[int64][]string{}
	seenAuthor := map[int64]bool{}
	for i, c := range candidates {
		if c.Document.Ref.Type != Broadcast {
			continue
		}
		id := c.Document.Ref.ID
		if _, ok := channels[id]; !ok {
			items = append(items, id)
			channels[id] = []string{}
		}
		for _, ch := range c.Document.Channels {
			channels[id] = appendUnique(channels[id], ch)
		}
		if !c.Score.Eligible {
			continue
		}
		indices = append(indices, i)
		if !seenAuthor[c.Document.AuthorID] {
			seenAuthor[c.Document.AuthorID] = true
			authors = append(authors, c.Document.AuthorID)
		}
	}
	if len(indices) == 0 {
		return false
	}
	if !state.started {
		if err := state.start(contexts, profile); err != nil {
			return fallback(err.Error())
		}
	}
	readCtx, cancel := context.WithTimeout(ctx, ratingEvidenceTimeout)
	err := state.fetch(readCtx, src, e.PGCAuthors, owner, items, authors)
	cancel()
	if err != nil {
		return fallback("evidence_unavailable")
	}
	vectors := make([][]float64, 0, len(indices))
	for _, i := range indices {
		c := candidates[i]
		v, ok := ratingVector(c, state.similarity(c), channels[c.Document.Ref.ID], state.pgc[c.Document.AuthorID])
		if !ok {
			return fallback("invalid_features")
		}
		vectors = append(vectors, v)
	}
	scores, ok := e.RatingLearned.ScoreBatch(vectors)
	if !ok || len(scores) != len(indices) {
		return fallback("unavailable")
	}
	for _, s := range scores {
		if s.Contract != discoverylr.RatingContract || s.ModelVersion != scores[0].ModelVersion || !finite(s.Probability) || s.Probability < 0 || s.Probability > 1 {
			return fallback("invalid_scores")
		}
	}
	metrics.DiscoveryLRScoring.WithLabelValues("rating_scored").Inc()
	for j, i := range indices {
		result := scores[j]
		candidates[i].LR = &result
		candidates[i].RatingFeatures = vectors[j]
		candidates[i].FinalScore = result.Probability
	}
	return true
}
