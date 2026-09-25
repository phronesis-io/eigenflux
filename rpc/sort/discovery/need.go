package discovery

import (
	"context"
	"errors"
	"strings"

	"eigenflux_server/pkg/need"
)

type NeedReader interface {
	Current(context.Context, int64, int64) (need.Snapshot, error)
	Active(context.Context, int64, []string, int64) ([]need.Snapshot, error)
	CheckIntent(context.Context, int64, int64, int64) error
}

func needError(err error) error {
	switch {
	case errors.Is(err, need.ErrNotFound):
		return Failure(404, "need_not_found")
	case errors.Is(err, need.ErrStaleIntent):
		return Failure(409, "inactive_need")
	default:
		return err
	}
}

func (cc *Compiler) Need(ctx context.Context, owner, id, now int64, snapshot need.Snapshot) (Context, error) {
	in, n := snapshot.Input, snapshot.Normalized
	if err := need.Validate(in); err != nil {
		return Context{}, Invalid("need", err.Error())
	}
	if n.Constraints.DeadlineMS != nil && *n.Constraints.DeadlineMS <= now {
		return Context{}, Failure(409, "expired_need")
	}
	if len(n.UnresolvedConstraints.Lang) > 0 || len(n.UnresolvedConstraints.ProviderRegion) > 0 {
		return Context{}, Failure(409, "unresolved_need_constraints")
	}
	f := Filters{BudgetMaxFen: n.Constraints.BudgetMaxFen, Currency: n.Constraints.Currency,
		MaxDurationMS: n.Constraints.MaxDurationMS, DeadlineMS: n.Constraints.DeadlineMS,
		ProviderRegion: n.Constraints.ProviderRegion, Lang: n.Constraints.Lang, ExcludeTerms: n.Constraints.ExcludeTerms}
	origin := "normalized_need"
	if snapshot.InputID == 0 {
		origin = "inline_need"
	}
	text := append([]string{n.Desc}, n.CandidateNeeds...)
	if in.Preferences != "" {
		text = append(text, in.Preferences)
	}
	c, err := cc.compileBase(owner, id, now, origin, strings.Join(text, "\n"), []Kind{Kind(in.NeedType)}, f)
	if err != nil {
		return c, err
	}
	c.CompilerVersion = "normalized_need_context_v1"
	c.CapturedNeed = &snapshot
	c.SourceNeedID, c.SourceNeedRevision = snapshot.InputID, snapshot.IntentVersion
	if in.Priority != nil {
		c.Priority = *in.Priority
	}
	for name := range c.Origins {
		if name != "exclude_authors" {
			c.Origins[name] = "normalized_need"
		}
	}
	// A vocabulary generation mismatch cannot reinterpret IDs in another taxonomy.
	// Keep text recall available; vocabulary coverage never gates a captured Need.
	for _, phrase := range n.CandidateNeeds {
		if mapped := n.MappedNeeds[phrase]; mapped != "" {
			if snapshot.TaxonomyVersion == cc.Taxonomy.Version && cc.Taxonomy.Intent(mapped, "", "") {
				c.SoftIntents = appendUnique(c.SoftIntents, mapped)
			} else {
				c.Warnings = appendUnique(c.Warnings, "need_mapping_unavailable")
			}
		}
	}
	if len(c.SoftIntents) == 0 {
		c.Warnings = append(c.Warnings, "intents_unmapped")
	}
	// Capture has already normalized the input. Only retrieval embedding is optional here.
	if err = cc.embed(ctx, &c); err != nil {
		return c, err
	}
	return c, nil
}

func (e *Engine) needContext(ctx context.Context, owner, now int64, snapshot need.Snapshot) (Context, error) {
	id, err := e.IDs.NextID()
	if err != nil {
		return Context{}, err
	}
	return e.Compiler.Need(ctx, owner, id, now, snapshot)
}
