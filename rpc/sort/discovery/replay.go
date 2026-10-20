package discovery

import "context"

// ReplayContexts runs the same recall, hydration, seen filtering, hard filters,
// ranking and page selection as Execute using already compiled real contexts.
// The caller supplies snapshot Sources and ranker/policy settings. This method
// neither compiles contexts nor records delivery. Friend contexts must be supplied
// explicitly; no extra lane is manufactured during replay.
func (e *Engine) ReplayContexts(ctx context.Context, owner int64, contexts []Context, kinds []Kind, limit int, now int64) (Execution, error) {
	return e.replayContexts(ctx, owner, contexts, kinds, limit, now, false)
}

// ReplayPrefetchContexts reproduces the 200-candidate Sort snapshot requested
// by Feed. The caller must apply the normal per-page source limits.
func (e *Engine) ReplayPrefetchContexts(ctx context.Context, owner int64, contexts []Context, kinds []Kind, limit int, now int64) (Execution, error) {
	return e.replayContexts(ctx, owner, contexts, kinds, limit, now, true)
}
func (e *Engine) replayContexts(ctx context.Context, owner int64, contexts []Context, kinds []Kind, limit int, now int64, prefetch bool) (Execution, error) {
	if len(contexts) == 0 {
		return Execution{Mode: Recommendation, Status: "insufficient_context", Candidates: []Candidate{}}, nil
	}
	for _, c := range contexts {
		if c.OwnerID != owner || c.ID <= 0 || !c.Active(now) {
			return Execution{}, Invalid("contexts", "invalid_replay_context")
		}
		for _, k := range c.Kinds {
			if !hasKind(kinds, k) {
				return Execution{}, Invalid("contexts", "out_of_scope_kind")
			}
		}
	}
	replay := *e
	replay.frozenContexts = append([]Context(nil), contexts...)
	replay.FriendFeedEnabled = false
	return replay.Execute(ctx, owner, Request{SourceKinds: kinds, Limit: limit, Prefetch: prefetch}, Recommendation, now)
}

// ReplayRecallQuery exposes the production lexical query with a pinned clock
// for bounded, read-only pool capture. It does not execute a search or delivery.
func ReplayRecallQuery(c Context, groups, ids []int64, now int64) (map[string]any, error) {
	return ReplayChannelQuery(c, "lexical", 80, groups, ids, now)
}

// ReplayChannelQuery pins retrieval time while preserving the serving query.
func ReplayChannelQuery(c Context, channel string, limit int, groups, ids []int64, now int64) (map[string]any, error) {
	c.retrievalAt = now
	c.excludeConsumedGroups, c.excludeRecalledIDs = groups, ids
	return Query(c, Broadcast, channel, limit)
}

// ReplayConsumptionReadScripts lets read-only capture clients allowlist the
// exact production Lua bodies; arbitrary EVAL remains prohibited.
func ReplayConsumptionReadScripts() (string, string) { return boundedGroupHistory, confirmConsumedLua }
