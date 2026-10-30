package discovery

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"eigenflux_server/pkg/bloomfilter"
	"eigenflux_server/pkg/impr"
	"eigenflux_server/pkg/metrics"
)

const (
	// History bounds are independent of an Agent's lifetime consumption volume.
	MaxRefillGroups   = 8192
	maxRefillHistory  = 65536
	maxRefillContexts = 10
	refillTimeout     = 750 * time.Millisecond
)

// RefillSources is optional so disabling the experiment retains the original
// Sources contract. Every returned group must pass the rolling delivery dedup.
type RefillSources interface {
	ConsumedGroups(context.Context, int64) (groups []int64, complete bool, err error)
	RecallUnseen(context.Context, Context, []int64, []int64, int) ([]Document, error)
}

type RefillReport struct {
	HistoryComplete bool `json:"history_complete"`
	LimitReached    bool `json:"limit_reached"`
	Groups          int  `json:"groups"`
	Contexts        int  `json:"contexts"`
	Recalled        int  `json:"recalled"`
	Eligible        int  `json:"eligible"`
}

type refillContext struct {
	context    Context
	documents  map[string]Document
	lexicalIDs []int64
}

func consumedRefillEnabled(owner int64, bps int) bool {
	return bucketEnabled(consumedRefillSalt, owner, bps)
}

// Atomic bound check prevents a concurrent impression writer from growing the
// set between SCARD and SMEMBERS. This script performs no writes.
const boundedGroupHistory = `local n = redis.call('SCARD', KEYS[1])
if n > tonumber(ARGV[1]) then return false end
return redis.call('SMEMBERS', KEYS[1])`

func (s *Source) ConsumedGroups(ctx context.Context, owner int64) ([]int64, bool, error) {
	if s.DisableDedup {
		return nil, true, nil
	}
	if owner <= 0 || s.Redis == nil {
		return nil, false, fmt.Errorf("consumption history unavailable")
	}
	raw, err := s.Redis.EvalRO(ctx, boundedGroupHistory, []string{fmt.Sprintf(impr.KeyGroupIDs, owner)}, maxRefillHistory).StringSlice()
	if err != nil {
		return nil, false, err
	}
	ids := make([]int64, 0, len(raw))
	for _, value := range raw {
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id <= 0 {
			return nil, false, fmt.Errorf("invalid consumption group")
		}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] > ids[j] })
	complete := len(ids) <= MaxRefillGroups
	if !complete {
		ids = ids[:MaxRefillGroups]
	}
	// The personal set has a refreshed whole-key TTL and is not itself a
	// rolling-30-day consumption authority. Never exclude unverified members.
	seen, err := s.confirmConsumedGroups(ctx, owner, ids)
	if err != nil {
		return nil, false, err
	}
	out := make([]int64, 0, len(seen))
	for _, id := range ids {
		if seen[id] {
			out = append(out, id)
		}
	}
	return out, complete, nil
}

// Keep each group on the wire once. Progressively removing matches also bounds
// Redis work to 30 x 8192 membership checks, in blocks of at most 512 values.
const confirmConsumedLua = `local remaining = ARGV
local found = {}
for _, key in ipairs(KEYS) do
 local next = {}
 for start = 1, #remaining, 512 do
  local block = {}
  for i = start, math.min(start + 511, #remaining) do block[#block+1] = remaining[i] end
  local hits = redis.call('SMISMEMBER', key, unpack(block))
  for i, value in ipairs(block) do
   if hits[i] == 1 then found[#found+1] = value else next[#next+1] = value end
  end
 end
 remaining = next
 if #remaining == 0 then break end
end
return found`

func (s *Source) confirmConsumedGroups(ctx context.Context, owner int64, ids []int64) (map[int64]bool, error) {
	out := map[int64]bool{}
	if len(ids) == 0 {
		return out, nil
	}
	if len(ids) > MaxRefillGroups {
		return nil, fmt.Errorf("consumption verification exceeds bound")
	}
	keys := make([]string, bloomfilter.BloomFilterLookbackDays)
	now := time.Now()
	for i := range keys {
		keys[i] = bloomfilter.GetKeyForDate(now.AddDate(0, 0, -i))
	}
	prefix := strconv.FormatInt(owner, 10) + ":"
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = prefix + strconv.FormatInt(id, 10)
	}
	hits, err := s.Redis.EvalRO(ctx, confirmConsumedLua, keys, args...).StringSlice()
	if err != nil {
		return nil, err
	}
	for _, member := range hits {
		if !strings.HasPrefix(member, prefix) {
			return nil, fmt.Errorf("invalid consumption owner")
		}
		id, err := strconv.ParseInt(strings.TrimPrefix(member, prefix), 10, 64)
		if err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, nil
}

func (s *Source) RecallUnseen(ctx context.Context, c Context, groups, ids []int64, limit int) ([]Document, error) {
	if s.DisabledChannels["lexical"] {
		return nil, nil
	}
	c.excludeConsumedGroups, c.excludeRecalledIDs = groups, ids
	return s.search(ctx, c, Broadcast, "lexical", limit)
}

func (e *Engine) refill(ctx context.Context, owner int64, states []refillContext, seenHead map[int64]bool, now int64,
	evaluate func(context.Context, Context, map[string]Document, []Document) ([]Candidate, error)) ([]Candidate, *RefillReport, []string, error) {
	source, ok := e.Sources.(RefillSources)
	if !ok {
		return nil, nil, []string{"consumed_refill:unsupported"}, nil
	}
	report := &RefillReport{}
	reasons := []string{}
	type result struct {
		state refillContext
		docs  []Document
		err   error
	}
	results := []result{}
	for _, state := range states {
		c := state.context
		if !hasKind(c.Kinds, Broadcast) || c.UnverifiedNeedReason != "" || c.Origin == "friend" || c.Origin == "baseline" {
			continue
		}
		if len(results) == maxRefillContexts {
			reasons = append(reasons, "consumed_refill:context_budget")
			break
		}
		results = append(results, result{state: state})
	}
	report.Contexts = len(results)
	if len(results) == 0 {
		return nil, nil, nil, nil
	}
	optional, cancel := context.WithTimeout(ctx, refillTimeout)
	defer cancel()
	groups, complete, err := source.ConsumedGroups(optional, owner)
	if err != nil {
		if ctx.Err() != nil {
			return nil, report, nil, ctx.Err()
		}
		metrics.DiscoveryRefill.WithLabelValues("history_unavailable").Inc()
		return nil, report, []string{"consumed_refill:history_unavailable"}, nil
	}
	if len(groups) > MaxRefillGroups {
		return nil, report, nil, fmt.Errorf("consumption history exceeds bound")
	}
	// Prioritize groups already rejected by the authoritative Seen check in
	// this request, even when they fall outside the bounded history prefix.
	prioritized := make([]int64, 0, len(seenHead))
	for id := range seenHead {
		prioritized = append(prioritized, id)
	}
	sort.Slice(prioritized, func(i, j int) bool { return prioritized[i] > prioritized[j] })
	if len(prioritized) > MaxRefillGroups {
		return nil, report, nil, fmt.Errorf("seen head exceeds bound")
	}
	for _, id := range groups {
		if seenHead[id] {
			continue
		}
		if len(prioritized) == MaxRefillGroups {
			complete = false
			break
		}
		prioritized = append(prioritized, id)
	}
	groups = prioritized
	report.HistoryComplete, report.Groups = complete, len(groups)
	if !complete {
		reasons = append(reasons, "consumed_refill:history_truncated")
	}
	sem := make(chan struct{}, 6)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-optional.Done():
				results[i].err = optional.Err()
				return
			}
			c := results[i].state.context
			c.retrievalAt = now
			results[i].docs, results[i].err = source.RecallUnseen(optional, c, groups, results[i].state.lexicalIDs, 80)
		}(i)
	}
	wg.Wait()
	if ctx.Err() != nil {
		return nil, report, nil, ctx.Err()
	}
	out := []Candidate{}
	for _, res := range results {
		if res.err != nil {
			reasons = append(reasons, "consumed_refill:lexical_unavailable")
			metrics.DiscoveryRefill.WithLabelValues("lexical_unavailable").Inc()
			continue
		}
		if len(res.docs) > 80 {
			return nil, report, nil, fmt.Errorf("refill source exceeds bound")
		}
		report.Recalled += len(res.docs)
		report.LimitReached = report.LimitReached || len(res.docs) == 80
		merged := map[string]Document{}
		docs := make([]Document, 0, len(res.docs))
		for _, d := range res.docs {
			if d.Ref.Type != Broadcast {
				return nil, report, nil, fmt.Errorf("unexpected refill kind")
			}
			if prior, ok := res.state.documents[d.Ref.Key()]; ok {
				d.Channels = append([]string(nil), prior.Channels...)
				if prior.SourceIndex == d.SourceIndex && prior.Version == d.Version && prior.ProjectionVersion == d.ProjectionVersion {
					d.DenseScore = prior.DenseScore
				}
			}
			d.Channels = appendUnique(d.Channels, "lexical")
			merged[d.Ref.Key()] = d
			docs = append(docs, d)
		}
		eligible, err := evaluate(optional, res.state.context, merged, docs)
		if err != nil {
			if ctx.Err() != nil {
				return nil, report, nil, ctx.Err()
			}
			metrics.DiscoveryRefill.WithLabelValues("validation_unavailable").Inc()
			reasons = append(reasons, "consumed_refill:validation_unavailable")
			continue
		}
		out = append(out, eligible...)
	}
	report.Eligible = len(out)
	metrics.DiscoveryRefill.WithLabelValues("attempted").Inc()
	return out, report, reasons, nil
}
