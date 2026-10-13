package discovery

import "context"

// ReplayContexts runs the same recall, hydration, seen filtering, hard filters,
// ranking and page selection as Execute using already compiled real contexts.
// The caller supplies snapshot Sources and ranker/policy settings. This method
// neither compiles contexts nor records delivery. Friend contexts must be supplied
// explicitly; no extra lane is manufactured during replay.
func (e *Engine) ReplayContexts(ctx context.Context, owner int64, contexts []Context, kinds []Kind, limit int, now int64) (Execution, error) {
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
	return replay.Execute(ctx, owner, Request{SourceKinds: kinds, Limit: limit}, Recommendation, now)
}
