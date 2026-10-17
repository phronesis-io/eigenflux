package discovery

import (
	"context"
	"eigenflux_server/pkg/featureindex"
	"eigenflux_server/pkg/metrics"
	"eigenflux_server/pkg/need"
	"eigenflux_server/pkg/recallsource"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

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
	// frozenContexts is set only by the offline replay entry point.
	frozenContexts    []Context
	Learned           LearnedRanker
	FriendFeedEnabled bool
	ConsumedRefillBPS int
	// ProfileSemanticBPS selects Agents whose broadcast recommendations use
	// profile-vector recall, gating and ordering.
	ProfileSemanticBPS int
	SourceLimits       []SourceLimit
	Compiler           *Compiler
	Needs              NeedReader
	IDs                IDGenerator
	Sources            Sources
	Rules              Rules
	Policies           func(context.Context, []Candidate, Mode, int) ([]Candidate, error)
}
type Execution struct {
	SourceLimits   []SourceLimit `json:"source_limits,omitempty"`
	Mode           Mode          `json:"mode"`
	Contexts       []Context     `json:"contexts"`
	Candidates     []Candidate   `json:"candidates"`
	Status         string        `json:"status"`
	PartialReasons []string      `json:"partial_reasons,omitempty"`
	FallbackReason string        `json:"fallback_reason,omitempty"`
	Refill         *RefillReport `json:"refill,omitempty"`
}

func (e *Engine) contexts(ctx context.Context, owner int64, r Request, mode Mode, now int64) ([]Context, string, error) {
	if e.frozenContexts != nil {
		return e.frozenContexts, "", nil
	}
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
	var captured []Context
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
		covered := map[Kind]bool{}
		for _, snapshot := range active {
			c, err := e.needContext(ctx, owner, now, snapshot)
			if err != nil {
				return nil, "", err
			}
			captured = append(captured, c)
			for _, kind := range c.Kinds {
				covered[kind] = true
			}
		}
		missing := []Kind{}
		for _, kind := range r.SourceKinds {
			if !covered[kind] {
				missing = append(missing, kind)
			}
		}
		if len(missing) == 0 {
			return captured, "", nil
		}
		r.SourceKinds = missing
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
	if len(captured) > 0 {
		reason = "missing_kind_needs"
	}
	if len(clauses) == 0 {
		if !hasKind(r.SourceKinds, Broadcast) {
			return captured, "empty_agent_context", nil
		}
		clauses = []string{""}
		origin = "baseline"
		reason = "empty_agent_context"
		r.SourceKinds = []Kind{Broadcast}
	}
	if len(clauses) > 5 {
		clauses = clauses[:5]
	}
	out := captured
	for _, query := range clauses {
		id, err := e.IDs.NextID()
		if err != nil {
			return nil, "", err
		}
		r.Query = query
		r.SourceRevision = info.Revision
		c, err := e.Compiler.Query(ctx, owner, id, now, r, origin)
		if err != nil {
			return nil, "", err
		}
		out = append(out, c)
	}
	return out, reason, nil
}
func (e *Engine) Execute(ctx context.Context, owner int64, r Request, mode Mode, now int64) (x Execution, resultErr error) {
	started := time.Now()
	ctx = featureindex.WithRequestCache(ctx)
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
	if mode == Recommendation {
		x.SourceLimits = e.SourceLimits
	}
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
	if mode == Recommendation && e.FriendFeedEnabled && hasKind(r.SourceKinds, Broadcast) {
		id, err := e.IDs.NextID()
		if err != nil {
			return x, err
		}
		// Friendship is a user-level source, independent of every captured Need.
		// Only request-level filters constrain this parallel lane.
		plan := compileBase(owner, "friend", "", []Kind{Broadcast}, r.Filters, false)
		friend, err := plan.execution(owner, id, now, "")
		if err != nil {
			return x, err
		}
		friend.SpecHash = hashContext(friend)
		contexts = append(contexts, friend)
	}
	x.Contexts = contexts
	x.FallbackReason = fallback
	// Missing context is an empty discovery result, not a transport error.
	// Feed must still assemble its other response fields.
	if len(contexts) == 0 {
		x.Status = "insufficient_context"
		return x, nil
	}
	// While the experiment runs, control requests also record profile cosine
	// as a shadow feature without changing eligibility or ordering.
	var profileVector []float32
	treated := mode == Recommendation && profileSemanticEnabled(owner, e.ProfileSemanticBPS)
	shadow := mode == Recommendation && e.ProfileSemanticBPS > 0 && !treated
	if treated || shadow {
		reason := ""
		if src, ok := e.Sources.(ProfileSemanticSources); !ok {
			reason = "profile_semantic:unsupported"
		} else if v, err := src.ProfileVector(ctx, owner); err != nil {
			reason = "profile_semantic:unavailable"
		} else if len(v) == 0 {
			reason = "profile_semantic:no_vector"
		} else {
			profileVector = v
		}
		if treated && reason != "" {
			x.PartialReasons = append(x.PartialReasons, reason)
		}
	}
	_, supported := e.Sources.(ProfileSemanticSources)
	// Need similarity uses the Need's own vector and does not depend on a
	// profile vector; Profile similarity and recall do.
	needOn := treated && supported
	semanticOn := treated && len(profileVector) > 0
	shadowOn := shadow && len(profileVector) > 0
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
	// Swing seeds depend on the user. Fetch once, then evaluate candidates
	// against each recommendation context. Friend recall has its own context.
	type userRecall struct {
		once sync.Once
		docs []Document
		err  error
	}
	userRecalls := map[string]*userRecall{"swing_i2i": {}, profileDenseKey: {}, profileFreshKey: {}}
	sem := make(chan struct{}, 6)
	for ci, c := range contexts {
		c.retrievalAt = now
		if !c.Active(now) {
			return x, Failure(409, "inactive_context")
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
			profileDense := semanticOn && kind == Broadcast && c.Origin == "agent_context" && len(c.Vector) == 0
			if len(c.Vector) > 0 || profileDense {
				channels = append(channels, "dense")
			}
			if kind == Broadcast && mode == Recommendation {
				channels = append(channels, "hot_recall", "new_recall", "new_ugc_recall", "swing_i2i")
			}
			if c.Origin == "baseline" {
				channels = []string{"hot_recall", "new_recall", "swing_i2i"}
			}
			if c.Origin == "friend" {
				channels = []string{"friend"}
			}
			if kind == Commission && r.CommissionID > 0 {
				channels = []string{"exact"}
			}
			lanes := [][2]string{}
			for _, channel := range channels {
				key := channel
				if channel == "dense" && profileDense {
					key = profileDenseKey
				}
				lanes = append(lanes, [2]string{channel, key})
			}
			if profileDense {
				// A recent-only lane keeps fresh matches from being crowded out by
				// older, more similar content.
				lanes = append(lanes, [2]string{"dense", profileFreshKey})
			}
			for _, lane := range lanes {
				ci, c, kind, channel, shareKey := ci, c, kind, lane[0], lane[1]
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
					if channel == "friend" || channel == "swing_i2i" {
						limit = MaxSnapshotCandidates
					}
					if strings.HasSuffix(channel, "recall") {
						limit = 20
					}
					if channel == "new_ugc_recall" {
						limit = 10
					}
					since := int64(0)
					switch shareKey {
					case profileDenseKey:
						limit = profileDenseLimit
					case profileFreshKey:
						limit, since = profileFreshLimit, now-profileFreshDays*day
					}
					var docs []Document
					var err error
					if shared := userRecalls[shareKey]; shared != nil {
						shared.once.Do(func() {
							if shareKey == profileDenseKey || shareKey == profileFreshKey {
								// One profile kNN per lane and request; each clause applies its own filters after recall.
								shared.docs, shared.err = e.Sources.(ProfileSemanticSources).RecallProfile(ctx, c, profileVector, since, limit)
								// Profile similarity is filled for every candidate below; a
								// profile kNN score is not Need/query semantic evidence.
								for i := range shared.docs {
									shared.docs[i].DenseScore = nil
								}
								return
							}
							shared.docs, shared.err = e.Sources.Recall(ctx, Context{OwnerID: owner}, kind, channel, limit)
						})
						docs, err = append([]Document(nil), shared.docs...), shared.err
						for i := range docs {
							docs[i].NeedExclusionText = len(c.Filters.ExcludeTerms) > 0
						}
					} else {
						docs, err = e.Sources.Recall(ctx, c, kind, channel, limit)
					}
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
	recalled := map[int64]recallsource.Source{}
	anySuccess := false
	below, exhausted := false, false
	refillOn := mode == Recommendation && consumedRefillEnabled(owner, e.ConsumedRefillBPS)
	var states []refillContext
	var seenHeadGroups map[int64]bool
	if refillOn {
		states = make([]refillContext, len(contexts))
		seenHeadGroups = map[int64]bool{}
	}
	// Request-local cosine by vector (the profile, or one captured Need) and
	// broadcast ID; NaN marks an item without an embedding.
	semanticCos := map[string]map[int64]float64{}
	semanticFailed := map[string]bool{}
	var semanticMu sync.Mutex
	vectorFor := func(c Context) (key string, vector []float32, need bool) {
		switch {
		case c.CapturedNeed != nil:
			if needOn && len(c.Vector) > 0 {
				return fmt.Sprintf("need:%d", c.ID), c.Vector, true
			}
		case c.Origin != "baseline" && (semanticOn || shadowOn):
			return "profile", profileVector, false
		}
		return "", nil, false
	}
	fillCosine := func(fillCtx context.Context, c Context, docs []Document) {
		key, vector, need := vectorFor(c)
		if key == "" {
			return
		}
		semanticMu.Lock()
		defer semanticMu.Unlock()
		if semanticFailed[key] {
			return
		}
		known := semanticCos[key]
		if known == nil {
			known = map[int64]float64{}
			semanticCos[key] = known
		}
		missing, queued := []int64{}, map[int64]bool{}
		for _, d := range docs {
			if _, ok := known[d.Ref.ID]; d.Ref.Type == Broadcast && !ok && !queued[d.Ref.ID] {
				queued[d.Ref.ID] = true
				missing = append(missing, d.Ref.ID)
			}
		}
		if len(missing) == 0 {
			return
		}
		readCtx, cancel := context.WithTimeout(fillCtx, cosineTimeout)
		got, err := e.Sources.(ProfileSemanticSources).Cosine(readCtx, vector, missing)
		cancel()
		if err != nil {
			// Candidates without similarity evidence keep the ordinary rule score;
			// the request does not retry the read for later contexts.
			semanticFailed[key] = true
			if need {
				x.PartialReasons = append(x.PartialReasons, "need_semantic:cosine_unavailable")
			} else if semanticOn {
				x.PartialReasons = append(x.PartialReasons, "profile_semantic:cosine_unavailable")
			}
			return
		}
		for _, id := range missing {
			v, ok := got[id]
			if !ok {
				v = math.NaN()
			}
			known[id] = v
		}
	}
	cosineOf := func(c Context, id int64) (float64, bool, bool) {
		key, _, need := vectorFor(c)
		semanticMu.Lock()
		defer semanticMu.Unlock()
		v, ok := semanticCos[key][id]
		return v, ok && key != "", need
	}
	evaluate := func(evalCtx context.Context, c Context, merged map[string]Document, docs []Document) ([]Candidate, error) {
		eligible := []Candidate{}
		fillCosine(evalCtx, c, docs)
		fresh, err := e.Sources.Hydrate(evalCtx, owner, mode, docs)
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		if mode == Recommendation {
			seen, err = e.Sources.Seen(evalCtx, owner, fresh)
			if err != nil {
				return nil, err
			}
		}
		for _, d := range fresh {
			prior, ok := merged[d.Ref.Key()]
			if !ok {
				return nil, Failure(503, "unexpected_hydrated_source")
			}
			if prior.Version != d.Version && !(d.Ref.Type == Broadcast && prior.Version == "") {
				metrics.DiscoveryRejected.WithLabelValues(string(d.Ref.Type), "source_version").Inc()
				continue
			}
			// Recall text is tied to the version just validated. Pool-only rows
			// obtain their text from the bounded DB presentation read instead.
			if prior.Version != "" {
				d.Text = prior.Text
				if d.Ref.Type != Agent {
					d.Preview = prior.Preview
				}
			}
			d.Lexical = prior.Lexical
			d.DenseScore = prior.DenseScore
			d.Channels = prior.Channels
			d.ExactMatch = prior.ExactMatch
			if mode == Recommendation {
				sources := d.RecallSources()
				for _, name := range recallsource.Names(sources &^ recalled[d.Ref.ID]) {
					metrics.RecallFeedTotal.WithLabelValues(name).Inc()
				}
				recalled[d.Ref.ID] |= sources
			}
			if seen[d.Ref.Key()] {
				if refillOn && d.Ref.Type == Broadcast && d.GroupID > 0 {
					seenHeadGroups[d.GroupID] = true
				}
				exhausted = true
				metrics.DiscoveryRejected.WithLabelValues(string(d.Ref.Type), "seen").Inc()
				continue
			}
			if reason := Check(c, d, mode, now); reason != "" {
				metrics.DiscoveryRejected.WithLabelValues(string(d.Ref.Type), reason).Inc()
				continue
			}
			d.Text = ""
			score := ScoreRules(c, d, e.Rules[d.Ref.Type][mode], now)
			friend := mode == Recommendation && d.RecallSources().Has(recallsource.Friend)
			if friend {
				score.Eligible = true
				score.Features["friend_relevance_bypass"] = 1
				score.Version += ":friend_feed_v1"
			}
			var rank *float64
			if cos, ok, need := cosineOf(c, d.Ref.ID); ok && d.Ref.Type == Broadcast {
				switch {
				case shadowOn:
					if !math.IsNaN(cos) {
						score.Features["profile_cosine"] = cos
					}
				case math.IsNaN(cos):
					// No item embedding: keep the rule decision, order after scored items.
					r := missingSemanticRank
					rank = &r
				default:
					var r float64
					score, r = scoreSemantic(score, d, cos, e.Rules[d.Ref.Type][mode], now, friend, need)
					rank = &r
				}
			}
			if !score.Eligible {
				below = true
				metrics.DiscoveryRejected.WithLabelValues(string(d.Ref.Type), "below_threshold").Inc()
			}
			if score.Eligible {
				candidate := Candidate{FinalScore: score.Value, Context: c, Document: d, Score: score, SemanticRank: rank}
				if rank != nil {
					candidate.FinalScore = *rank
				}
				eligible = append(eligible, candidate)
			}
		}
		return eligible, nil
	}
	type pendingContext struct {
		c      Context
		merged map[string]Document
		docs   []Document
	}
	pending := []pendingContext{}
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
					if prior.Ref.Type == Broadcast && prior.Version == "" && d.Version != "" {
						channels := prior.Channels
						prior = d
						prior.Channels = channels
					}
					prior.Channels = appendUnique(prior.Channels, res.channel)
					if d.Lexical > prior.Lexical {
						prior.Lexical = d.Lexical
					}
					if d.SourceIndex == prior.SourceIndex && d.Version == prior.Version && d.ProjectionVersion == prior.ProjectionVersion && d.DenseScore != nil && (prior.DenseScore == nil || *d.DenseScore > *prior.DenseScore) {
						prior.DenseScore = d.DenseScore
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
		if refillOn {
			states[ci] = refillContext{context: c, documents: merged}
			for _, res := range results {
				if res.ci == ci && res.kind == Broadcast && res.channel == "lexical" && res.err == nil {
					for _, d := range res.docs {
						states[ci].lexicalIDs = append(states[ci].lexicalIDs, d.Ref.ID)
					}
				}
			}
		}
		pending = append(pending, pendingContext{c, merged, docs})
	}
	if needOn || shadowOn {
		// One similarity read per vector: all Profile contexts share the profile.
		var profileContext *Context
		all := []Document{}
		for i, p := range pending {
			if key, _, need := vectorFor(p.c); key != "" && !need {
				profileContext = &pending[i].c
				all = append(all, p.docs...)
			} else if need {
				fillCosine(ctx, p.c, p.docs)
			}
		}
		if profileContext != nil {
			fillCosine(ctx, *profileContext, all)
		}
	}
	for _, p := range pending {
		eligible, err := evaluate(ctx, p.c, p.merged, p.docs)
		if err != nil {
			return x, err
		}
		candidates = append(candidates, eligible...)
	}
	if !anySuccess && len(results) > 0 {
		return x, Failure(503, "retrieval_unavailable")
	}
	selectCandidates := func(in []Candidate) ([]Candidate, error) {
		if len(in) == 0 {
			return []Candidate{}, nil
		}
		in = append([]Candidate(nil), in...)
		e.rankLearned(in, mode)
		if e.Policies != nil {
			var err error
			in, err = e.Policies(ctx, in, mode, resultLimit)
			if err != nil {
				return nil, err
			}
		}
		mergeLimit := resultLimit
		if mode == Recommendation && len(x.SourceLimits) > 0 {
			mergeLimit = MaxSnapshotCandidates
		}
		out := Merge(in, r.SourceKinds, mode, mergeLimit)
		if mode == Recommendation && !r.Prefetch {
			out, _ = SelectRecommendationPage(out, resultLimit, x.SourceLimits)
		}
		return out, nil
	}
	x.Candidates, err = selectCandidates(candidates)
	if err != nil {
		return x, err
	}
	pageSize := func() int {
		if mode == Recommendation && r.Prefetch {
			page, _ := SelectRecommendationPage(x.Candidates, r.Limit, x.SourceLimits)
			return len(page)
		}
		return len(x.Candidates)
	}
	// Recover an empty page only. Filling a short snapshot or partial page
	// would let new Need/Profile matches displace already deliverable content.
	if refillOn && pageSize() == 0 {
		additional, report, reasons, err := e.refill(ctx, owner, states, seenHeadGroups, now, evaluate)
		if err != nil {
			return x, err
		}
		x.Refill = report
		x.PartialReasons = append(x.PartialReasons, reasons...)
		if len(additional) > 0 {
			// Refill can add lexical evidence to a pool-only row. Replace that
			// context/item, retaining every other original eligible candidate.
			keys := map[string]bool{}
			key := func(c Candidate) string { return fmt.Sprintf("%d:%s", c.Context.ID, c.Document.Ref.Key()) }
			for _, c := range additional {
				keys[key(c)] = true
			}
			combined := append([]Candidate(nil), additional...)
			for _, c := range candidates {
				if !keys[key(c)] {
					combined = append(combined, c)
				}
			}
			x.Candidates, err = selectCandidates(combined)
			if err != nil {
				return x, err
			}
		}
		if report != nil && report.LimitReached && pageSize() < r.Limit {
			x.PartialReasons = append(x.PartialReasons, "consumed_refill:scan_budget")
			metrics.DiscoveryRefill.WithLabelValues("scan_budget").Inc()
		}
	}
	x.PartialReasons = unique(x.PartialReasons)
	if len(x.Candidates) == 0 {
		x.Status = "no_match"
		if exhausted {
			x.Status = "exhausted"
		}
		if below {
			x.Status = "below_threshold"
		}
		if len(candidates) > 0 {
			x.Status = "exhausted"
		}
		return x, nil
	}
	groupResultPages(x.Candidates, r.SourceKinds, r.Limit)
	x.Status = "ok"
	if len(x.Candidates) == 0 {
		x.Status = "exhausted"
	}
	return x, nil
}
func channelOrder(c string) int {
	for i, s := range []string{"lexical", "dense", "hot_recall", "new_recall", "new_ugc_recall", "friend", "swing_i2i"} {
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
