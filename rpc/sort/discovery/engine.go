package discovery

import (
	"context"
	"eigenflux_server/pkg/metrics"
	"eigenflux_server/pkg/need"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type ContextStore interface {
	Create(context.Context, Context) (Context, error)
}
type IDGenerator interface{ NextID() (int64, error) }
type OwnerContext struct {
	Revision  string
	Clauses   []string
	Languages []string
}
type Sources interface {
	Owner(context.Context, int64) (OwnerContext, error)
	Recall(context.Context, Context, Kind, string, int) ([]Document, error)
	Hydrate(context.Context, int64, Mode, []Document) ([]Document, error)
	Seen(context.Context, int64, []Document) (map[string]bool, error)
}
type Engine struct {
	Compiler *Compiler
	Needs    NeedReader
	Store    ContextStore
	IDs      IDGenerator
	Sources  Sources
	Rules    Rules
	Policies func(context.Context, []Candidate, Mode, int) ([]Candidate, error)
}
type Execution struct {
	Mode           Mode        `json:"mode"`
	Contexts       []Context   `json:"contexts"`
	Candidates     []Candidate `json:"candidates"`
	Status         string      `json:"status"`
	PartialReasons []string    `json:"partial_reasons,omitempty"`
	FallbackReason string      `json:"fallback_reason,omitempty"`
}

func (e *Engine) contexts(ctx context.Context, owner int64, r Request, mode Mode, now int64) ([]Context, string, error) {
	if mode == Search && r.NeedID != 0 {
		snapshot, err := e.Needs.Current(ctx, owner, r.NeedID)
		if err != nil {
			return nil, "", needError(err)
		}
		c, err := e.needContext(ctx, owner, now, snapshot)
		if err != nil {
			return nil, "", err
		}
		if r.KindsExplicit && (len(r.SourceKinds) != 1 || r.SourceKinds[0] != c.Kinds[0]) {
			return nil, "", Invalid("source_kinds", "need_kind_mismatch")
		}
		if !c.Active(now) {
			return nil, "", Failure(409, "inactive_need")
		}
		return []Context{c}, "", nil
	}
	if mode == Recommendation {
		if len(r.NeedIDs) > 0 {
			out := []Context{}
			seen := map[int64]bool{}
			for _, s := range r.NeedIDs {
				id, _ := strconv.ParseInt(s, 10, 64)
				if seen[id] {
					return nil, "", Invalid("need_ids", "duplicate")
				}
				seen[id] = true
				snapshot, err := e.Needs.Current(ctx, owner, id)
				if err != nil {
					return nil, "", needError(err)
				}
				c, err := e.needContext(ctx, owner, now, snapshot)
				if err != nil {
					return nil, "", err
				}
				if !c.Active(now) || !hasKind(r.SourceKinds, c.Kinds[0]) {
					return nil, "", Failure(409, "inactive_or_out_of_scope_need")
				}
				out = append(out, c)
			}
			return out, "", nil
		}
		kinds := make([]string, len(r.SourceKinds))
		for i, kind := range r.SourceKinds {
			kinds[i] = string(kind)
		}
		active, err := e.Needs.Active(ctx, owner, kinds, now)
		if err != nil {
			return nil, "", err
		}
		if len(active) > 0 {
			out := make([]Context, 0, len(active))
			for _, snapshot := range active {
				c, err := e.needContext(ctx, owner, now, snapshot)
				if err != nil {
					return nil, "", err
				}
				out = append(out, c)
			}
			return out, "", nil
		}
	}
	if mode == Search {
		id, err := e.IDs.NextID()
		if err != nil {
			return nil, "", err
		}
		var c Context
		if r.Need != nil {
			if r.KindsExplicit && (len(r.SourceKinds) != 1 || r.SourceKinds[0] != Kind(r.Need.NeedType)) {
				return nil, "", Invalid("source_kinds", "need_kind_mismatch")
			}
			if err = e.Needs.CheckIntent(ctx, owner, r.Need.IntentID, r.Need.IntentVersion); err != nil {
				return nil, "", needError(err)
			}
			raw, marshalErr := json.Marshal(r.Need)
			if marshalErr != nil {
				return nil, "", marshalErr
			}
			c, err = e.Compiler.Need(ctx, owner, id, now, need.Snapshot{Input: raw, IntentID: r.Need.IntentID, IntentVersion: r.Need.IntentVersion})
			if err != nil {
				return nil, "", err
			}
		} else {
			c, err = e.Compiler.Query(ctx, owner, id, now, r, "query")
			if err != nil {
				return nil, "", err
			}
		}
		return []Context{c}, "", nil
	}
	info, err := e.Sources.Owner(ctx, owner)
	if err != nil {
		return nil, "", err
	}
	clauses := info.Clauses
	origin, reason := "agent_context", "no_active_needs"
	if len(clauses) == 0 {
		if !hasKind(r.SourceKinds, Broadcast) {
			return nil, "empty_agent_context", nil
		}
		clauses = []string{""}
		origin = "baseline"
		reason = "empty_agent_context"
		r.SourceKinds = []Kind{Broadcast}
	}
	if len(clauses) > 5 {
		clauses = clauses[:5]
	}
	out := []Context{}
	for _, query := range clauses {
		id, err := e.IDs.NextID()
		if err != nil {
			return nil, "", err
		}
		r.Query = query
		c, err := e.Compiler.Query(ctx, owner, id, now, r, origin)
		if err != nil {
			return nil, "", err
		}
		c.SourceRevision = info.Revision
		out = append(out, c)
	}
	return out, reason, nil
}
func (e *Engine) Execute(ctx context.Context, owner int64, r Request, mode Mode, now int64) (x Execution, resultErr error) {
	started := time.Now()
	defer func() {
		status := x.Status
		if resultErr != nil {
			status = "error"
		}
		metrics.DiscoveryDuration.WithLabelValues(string(mode), status).Observe(time.Since(started).Seconds())
		if x.FallbackReason != "" {
			metrics.DiscoveryFallback.WithLabelValues(x.FallbackReason).Inc()
		}
	}()
	x = Execution{Mode: mode, Candidates: []Candidate{}}
	if owner <= 0 {
		return x, Failure(401, "unauthorized")
	}
	var err error
	r, err = NormalizeRequest(r, mode, now)
	if err != nil {
		return x, err
	}
	if r.Cursor != "" {
		return x, Invalid("cursor", "requires_feed_delivery")
	}
	resultLimit := r.Limit
	if r.Prefetch {
		resultLimit = MaxSnapshotCandidates
	}
	if err = e.Rules.Validate(); err != nil {
		return x, err
	}
	if r.Defaults.Language == "card" && len(r.Filters.Lang) == 0 {
		info, err := e.Sources.Owner(ctx, owner)
		if err != nil {
			return x, err
		}
		r.Filters.Lang = append([]string(nil), info.Languages...)
		r.InheritedLanguage = true
	}
	// Identity lookup precedes embedding and ES. Exact matches still pass the
	// ordinary authority hydration and hard filters below.
	var exactAgents []Document
	if mode == Search && r.Query != "" && hasKind(r.SourceKinds, Agent) {
		exactAgents, err = e.Sources.Recall(ctx, Context{Origin: "query", Query: r.Query, OwnerID: owner}, Agent, "exact", 100)
		if err != nil {
			return x, err
		}
		r.agentExact = len(exactAgents) > 0 || decimalAgentQuery(r.Query)
	}
	contexts, fallback, err := e.contexts(ctx, owner, r, mode, now)
	if err != nil {
		return x, err
	}
	if mode == Recommendation && !emptyFilters(r.Filters) {
		for i, c := range contexts {
			if c.CapturedNeed == nil {
				continue
			}
			filters, err := IntersectFilters(c.Filters, r.Filters)
			if err != nil {
				return x, err
			}
			if err = ValidateFilters(filters, c.Kinds, now); err != nil {
				return x, err
			}
			c.Filters = filters
			origins := map[string]string{}
			for field, origin := range c.Origins {
				origins[field] = origin
			}
			b, _ := json.Marshal(r.Filters)
			var requested map[string]json.RawMessage
			_ = json.Unmarshal(b, &requested)
			for field := range requested {
				origins[field] = "explicit_intersection"
			}
			if r.InheritedLanguage {
				origins["lang"] = "card_default_intersection"
			}
			c.Origins = origins
			c.SpecHash = hashContext(c)
			contexts[i] = c
		}
	}
	for i, c := range contexts {
		contexts[i], err = e.Store.Create(ctx, c)
		if err != nil {
			return x, err
		}
	}
	x.Contexts = contexts
	x.FallbackReason = fallback
	// Missing context is an empty discovery result, not a transport error.
	// Feed must still assemble its other response fields.
	if len(contexts) == 0 {
		x.Status = "insufficient_context"
		return x, nil
	}
	type channelResult struct {
		ci      int
		kind    Kind
		channel string
		docs    []Document
		err     error
	}
	results := []channelResult{}
	if r.agentExact {
		results = append(results, channelResult{ci: 0, kind: Agent, channel: "exact", docs: exactAgents})
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 6)
	for ci, c := range contexts {
		if !c.Active(now) {
			return x, Failure(409, "inactive_context")
		}
		if c.TaxonomyVersion != e.Compiler.Taxonomy.Version {
			return x, Failure(409, "stale_taxonomy")
		}
		x.PartialReasons = append(x.PartialReasons, c.Warnings...)
		if c.UnverifiedNeedReason != "" {
			continue
		}
		for _, kind := range c.Kinds {
			if kind == Agent && r.agentExact {
				continue
			}
			channels := []string{"lexical"}
			if c.QueryAnalysis != nil && len(c.QueryAnalysis.Expansions) > 0 {
				channels = append(channels, "synonym")
			}
			if len(c.Vector) > 0 {
				channels = append(channels, "dense")
			}
			if c.Filters.Category != "" || len(c.SoftIntents) > 0 {
				channels = append(channels, "structured")
			}
			if kind == Broadcast && mode == Recommendation {
				channels = append(channels, "hot_recall", "new_recall", "new_ugc_recall")
			}
			if c.Origin == "baseline" {
				channels = []string{"hot_recall", "new_recall"}
			}
			for _, channel := range channels {
				ci, c, kind, channel := ci, c, kind, channel
				wg.Add(1)
				go func() {
					defer wg.Done()
					select {
					case sem <- struct{}{}:
						defer func() { <-sem }()
					case <-ctx.Done():
						mu.Lock()
						results = append(results, channelResult{ci: ci, kind: kind, channel: channel, err: ctx.Err()})
						mu.Unlock()
						return
					}
					limit := 80
					if channel == "structured" || channel == "synonym" {
						limit = 40
					}
					if strings.HasSuffix(channel, "recall") {
						limit = 20
					}
					if channel == "new_ugc_recall" {
						limit = 10
					}
					docs, err := e.Sources.Recall(ctx, c, kind, channel, limit)
					mu.Lock()
					results = append(results, channelResult{ci, kind, channel, docs, err})
					mu.Unlock()
				}()
			}
		}
	}
	wg.Wait()
	if err = ctx.Err(); err != nil {
		return x, err
	}
	sort.Slice(results, func(i, j int) bool {
		a, b := results[i], results[j]
		if a.ci != b.ci {
			return a.ci < b.ci
		}
		if a.kind != b.kind {
			return kindPosition(r.SourceKinds, a.kind) < kindPosition(r.SourceKinds, b.kind)
		}
		return channelOrder(a.channel) < channelOrder(b.channel)
	})
	candidates := []Candidate{}
	anySuccess := false
	below, exhausted := false, false
	for ci, c := range contexts {
		if c.UnverifiedNeedReason != "" {
			continue
		}
		merged := map[string]Document{}
		order := []string{}
		perKind := map[Kind]int{}
		maxRows := 0
		for _, res := range results {
			if res.ci == ci {
				if res.err != nil {
					metrics.DiscoveryChannelFailures.WithLabelValues(string(res.kind), res.channel).Inc()
					x.PartialReasons = append(x.PartialReasons, fmt.Sprintf("%s:%s_unavailable", res.kind, res.channel))
				} else {
					anySuccess = true
				}
				if len(res.docs) > maxRows {
					maxRows = len(res.docs)
				}
			}
		}
		for pos := 0; pos < maxRows; pos++ {
			for _, res := range results {
				if res.ci != ci || res.err != nil || pos >= len(res.docs) {
					continue
				}
				d := res.docs[pos]
				key := d.Ref.Key()
				if prior, ok := merged[key]; ok {
					prior.Channels = appendUnique(prior.Channels, res.channel)
					if d.Lexical > prior.Lexical {
						prior.Lexical = d.Lexical
					}
					merged[key] = prior
					continue
				}
				cap := 100
				if d.Ref.Type == Broadcast {
					cap = 200
				}
				if perKind[d.Ref.Type] >= cap || len(order) >= 200 {
					continue
				}
				d.Channels = appendUnique(d.Channels, res.channel)
				merged[key] = d
				order = append(order, key)
				perKind[d.Ref.Type]++
			}
		}
		docs := make([]Document, 0, len(order))
		for _, key := range order {
			docs = append(docs, merged[key])
		}
		fresh, err := e.Sources.Hydrate(ctx, owner, mode, docs)
		if err != nil {
			return x, err
		}
		seen := map[string]bool{}
		if mode == Recommendation {
			seen, err = e.Sources.Seen(ctx, owner, fresh)
			if err != nil {
				return x, err
			}
		}
		for _, d := range fresh {
			prior, ok := merged[d.Ref.Key()]
			if !ok {
				return x, Failure(503, "unexpected_hydrated_source")
			}
			if prior.Version != d.Version {
				continue
			}
			d.Lexical = prior.Lexical
			d.Channels = prior.Channels
			d.ExactMatch = prior.ExactMatch
			if d.Ref.Type == Broadcast {
				d.Vector = prior.Vector
			}
			if seen[d.Ref.Key()] {
				exhausted = true
				metrics.DiscoveryRejected.WithLabelValues(string(d.Ref.Type), "seen").Inc()
				continue
			}
			if reason := Check(c, d, mode, now); reason != "" {
				metrics.DiscoveryRejected.WithLabelValues(string(d.Ref.Type), reason).Inc()
				continue
			}
			score := ScoreRules(c, d, e.Rules[d.Ref.Type][mode], now)
			if !score.Eligible {
				below = true
				metrics.DiscoveryRejected.WithLabelValues(string(d.Ref.Type), "below_threshold").Inc()
			}
			if score.Eligible {
				candidates = append(candidates, Candidate{FinalScore: score.Value, Context: c, Document: d, Score: score})
			}
		}
	}
	if !anySuccess && len(results) > 0 {
		return x, Failure(503, "retrieval_unavailable")
	}
	x.PartialReasons = unique(x.PartialReasons)
	if len(candidates) == 0 {
		x.Status = "no_match"
		if exhausted {
			x.Status = "exhausted"
		}
		if below {
			x.Status = "below_threshold"
		}
		return x, nil
	}
	if e.Policies != nil {
		candidates, err = e.Policies(ctx, candidates, mode, resultLimit)
		if err != nil {
			return x, err
		}
	}
	x.Candidates = Merge(candidates, r.SourceKinds, mode, resultLimit)
	x.Status = "ok"
	if len(x.Candidates) == 0 {
		x.Status = "exhausted"
	}
	return x, nil
}
func channelOrder(c string) int {
	for i, s := range []string{"lexical", "dense", "structured", "synonym", "hot_recall", "new_recall", "new_ugc_recall"} {
		if s == c {
			return i
		}
	}
	return 99
}
func appendUnique(xs []string, s string) []string {
	for _, x := range xs {
		if x == s {
			return xs
		}
	}
	return append(xs, s)
}
func unique(xs []string) []string {
	out := []string{}
	for _, s := range xs {
		out = appendUnique(out, s)
	}
	sort.Strings(out)
	return out
}
