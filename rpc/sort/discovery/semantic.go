package discovery

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"strings"
	"time"

	"eigenflux_server/pkg/cache"
	embcodec "eigenflux_server/pkg/embedding"
	"eigenflux_server/pkg/es"
	"eigenflux_server/pkg/integrationprofile"
)

const (
	profileSemanticSalt    = "discovery-profile-semantic-v1:"
	profileSemanticVersion = ":profile_semantic_v2"
	needSemanticVersion    = ":need_semantic_v1"
	profileDenseKey        = "profile_dense"
	profileDenseLimit      = 80
	profileFreshKey        = "profile_fresh"
	profileFreshLimit      = 40
	profileFreshDays       = 30
	maxCosineIDs           = 5000
	cosineTimeout          = 300 * time.Millisecond
	profileHistoryTimeout  = 300 * time.Millisecond
	profileFreshnessWeight = 0.05
	day                    = int64(24 * 60 * 60 * 1000)
)

// ProfileSemanticSources is optional so disabling the experiment retains the
// original Sources contract.
type ProfileSemanticSources interface {
	// ProfileVector returns the Agent's stored profile embedding, or nil when absent.
	ProfileVector(context.Context, int64) ([]float32, error)
	// ProfileCosine returns cosine(profile, item embedding) for broadcast IDs.
	// Items without an embedding are omitted.
	Cosine(context.Context, []float32, []int64) (map[int64]float64, error)
	// RecallProfile runs dense recall with the profile vector, excluding the
	// Agent's verified consumed groups when that history is available. A
	// positive since (epoch ms) restricts recall to content created after it.
	RecallProfile(ctx context.Context, c Context, vector []float32, since int64, limit int) ([]Document, error)
}

func bucketEnabled(salt string, owner int64, bps int) bool {
	if bps <= 0 || bps > 10000 {
		return false
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(salt))
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(owner))
	_, _ = h.Write(b[:])
	return h.Sum64()%10000 < uint64(bps)
}

const (
	needBudgetSalt     = "discovery-need-budget-v1:"
	consumedRefillSalt = "discovery-consumed-refill-v1:"
)

// experimentArms records the intent-to-treat arm of every running
// recommendation experiment, keyed by its salt without the trailing colon.
// The rating challenger's population is the rating treatment.
func (e *Engine) experimentArms(owner int64) map[string]string {
	arms := map[string]string{}
	assign := func(salt string, bps int) bool {
		if bps <= 0 {
			return false
		}
		on := bucketEnabled(salt, owner, bps)
		arm := "control"
		if on {
			arm = "treatment"
		}
		arms[strings.TrimSuffix(salt, ":")] = arm
		return on
	}
	assign(needBudgetSalt, e.NeedBudgetBPS)
	assign(consumedRefillSalt, e.ConsumedRefillBPS)
	assign(profileSemanticSalt, e.ProfileSemanticBPS)
	if assign(ratingLRSalt, e.RatingLRBPS) {
		assign(ratingNextSalt, e.RatingNextBPS)
	}
	if len(arms) == 0 {
		return nil
	}
	return arms
}

func profileSemanticEnabled(owner int64, bps int) bool {
	return bucketEnabled(profileSemanticSalt, owner, bps)
}

// profileMinCosine is the cosine floor by content age at which a candidate
// becomes eligible on similarity alone. Calibrated on rated recommendations:
// below 0.30 the -1 ratings outnumber positives for Profile and Need vectors;
// older content needs stronger similarity.
func profileMinCosine(freshAt, now int64) float64 {
	age := now - freshAt
	switch {
	case freshAt > 0 && age <= 30*day:
		return 0.30
	case freshAt > 0 && age <= 90*day:
		return 0.40
	default:
		return 0.50
	}
}

// Semantic tiers order scored candidates across contexts of equal priority:
// a candidate at its cosine floor, then a rule-eligible candidate below it,
// then a rule-eligible candidate without an item embedding. Ranks stay
// positive and banded by tier so multiplicative rerank boosts keep their
// direction.
const (
	semanticTierMissing = iota
	semanticTierBelowFloor
	semanticTierAtFloor
)

func semanticNames(need bool) (string, string) {
	if need {
		return "need", needSemanticVersion
	}
	return "profile", profileSemanticVersion
}

// scoreSemantic scores a candidate by its cosine to the profile or captured
// Need vector. Eligibility is additive: a candidate the rule score already
// accepts stays eligible, and a candidate reaching the age-tiered cosine floor
// becomes eligible (vector-only recall otherwise needs cosine near 0.7). The
// rule score's value, relevance and contributions are retained so replay
// samples keep the discovery LR feature contract.
func scoreSemantic(s Score, d Document, cos float64, rule Rule, now int64, bypass, need bool) (Score, float64, int) {
	floor := profileMinCosine(d.FreshAt, now)
	name, version := semanticNames(need)
	s.Version += version
	s.Features[name+"_cosine"] = cos
	s.Features[name+"_min_cosine"] = floor
	if cos >= floor {
		s.Eligible = true
		return s, 2 + cos + profileFreshnessWeight*decay(d.FreshAt, now, rule.HalfLifeMS), semanticTierAtFloor
	}
	s.Eligible = s.Eligible || bypass
	return s, 1 + (1+cos)/2*0.99, semanticTierBelowFloor
}

// scoreUnembedded keeps the rule decision for a candidate without an item
// embedding and ranks it after every similarity-scored candidate.
func scoreUnembedded(s Score, need bool) (Score, float64, int) {
	name, version := semanticNames(need)
	s.Version += version
	s.Features[name+"_embedding_missing"] = 1
	return s, math.Max(0, math.Min(1, s.Value)), semanticTierMissing
}

func (s *Source) ProfileVector(ctx context.Context, owner int64) ([]float32, error) {
	var raw []byte
	if s.ManagedIntegrations {
		managed, err := integrationprofile.LoadMatching(ctx, s.DB, owner)
		if err != nil {
			return nil, err
		}
		if managed != nil {
			raw = managed.Embedding
		}
	}
	if len(raw) == 0 && s.Redis != nil {
		cached, err := cache.NewEmbeddingCache(s.Redis, 0).Get(ctx, owner)
		if err != nil && !errors.Is(err, cache.ErrCacheMiss) {
			return nil, err
		}
		raw = cached
	}
	if len(raw) == 0 {
		var row struct{ ProfileEmbedding []byte }
		if err := s.DB.WithContext(ctx).Raw(`SELECT profile_embedding FROM agent_profiles WHERE agent_id=?`, owner).Scan(&row).Error; err != nil {
			return nil, err
		}
		raw = row.ProfileEmbedding
	}
	v := embcodec.Decode(raw)
	norm := 0.0
	for _, x := range v {
		f := float64(x)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fmt.Errorf("invalid profile embedding")
		}
		norm += f * f
	}
	if norm == 0 {
		return nil, nil
	}
	return v, nil
}

func (s *Source) Cosine(ctx context.Context, vector []float32, ids []int64) (map[int64]float64, error) {
	out := map[int64]float64{}
	if len(ids) == 0 {
		return out, nil
	}
	if len(ids) > maxCosineIDs {
		return nil, fmt.Errorf("cosine budget exceeded")
	}
	if es.Client == nil {
		return nil, fmt.Errorf("source index unavailable")
	}
	index := s.BroadcastIndex
	if index == "" {
		index = es.ReadIndexPattern
	}
	// Rollover generations may hold the same item; collapse keeps one hit per ID.
	body := map[string]any{"size": len(ids), "track_total_hits": false, "_source": []string{"id"}, "collapse": map[string]any{"field": "id"},
		"query": map[string]any{"script_score": map[string]any{
			"query":  map[string]any{"bool": map[string]any{"filter": []any{terms("id", ids), map[string]any{"exists": map[string]any{"field": "embedding"}}}}},
			"script": map[string]any{"source": "cosineSimilarity(params.v, 'embedding') + 1.0", "params": map[string]any{"v": vector}},
		}}}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	resp, err := es.Client.Search(es.Client.Search.WithContext(ctx), es.Client.Search.WithIndex(index), es.Client.Search.WithBody(bytes.NewReader(b)))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.IsError() {
		return nil, fmt.Errorf("cosine read failed: %d", resp.StatusCode)
	}
	var env struct {
		TimedOut bool `json:"timed_out"`
		Shards   struct {
			Failed int `json:"failed"`
		} `json:"_shards"`
		Hits struct {
			Hits []struct {
				Score  float64 `json:"_score"`
				Source struct {
					ID int64 `json:"id"`
				} `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&env); err != nil {
		return nil, err
	}
	if env.TimedOut || env.Shards.Failed > 0 {
		return nil, fmt.Errorf("incomplete cosine read")
	}
	for _, h := range env.Hits.Hits {
		cos := h.Score - 1
		if prior, ok := out[h.Source.ID]; !ok || cos > prior {
			out[h.Source.ID] = cos
		}
	}
	return out, nil
}

func (s *Source) RecallProfile(ctx context.Context, c Context, vector []float32, since int64, limit int) ([]Document, error) {
	if s.DisabledChannels["dense"] {
		return nil, nil
	}
	// Exclusion is best effort: a slow or failed history read keeps the recall.
	historyCtx, cancel := context.WithTimeout(ctx, profileHistoryTimeout)
	groups, _, err := s.ConsumedGroups(historyCtx, c.OwnerID)
	cancel()
	if err != nil {
		groups = nil
	}
	c.Vector, c.excludeConsumedGroups, c.excludeRecalledIDs, c.createdSince = vector, groups, nil, since
	return s.search(ctx, c, Broadcast, "dense", limit)
}
