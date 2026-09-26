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
	in, err := snapshot.ExecutionInput()
	if err != nil {
		return Context{}, Invalid("need", err.Error())
	}
	if in.Constraints.DeadlineMS != nil && *in.Constraints.DeadlineMS <= now {
		return Context{}, Failure(409, "expired_need")
	}
	constraints, resolved := need.ExecutionConstraints(in.Constraints)
	f := Filters{BudgetMaxFen: constraints.BudgetMaxFen, Currency: constraints.Currency,
		MaxDurationMS: constraints.MaxDurationMS, DeadlineMS: constraints.DeadlineMS,
		ProviderRegion: constraints.ProviderRegion, Lang: constraints.Lang, ExcludeTerms: constraints.ExcludeTerms}
	origin := "need_input"
	if snapshot.InputID == 0 {
		origin = "inline_need"
	}
	text := []string{in.Target.Goal, in.Target.Context}
	c, err := cc.compileBase(owner, id, now, origin, strings.Join(text, "\n"), []Kind{Kind(in.NeedType)}, f, false)
	if err != nil {
		return c, err
	}
	c.CompilerVersion = "need_input_context_v3"
	c.CapturedNeed = &snapshot
	c.SourceNeedID, c.SourceNeedRevision = snapshot.InputID, snapshot.IntentVersion
	if in.Priority != nil {
		c.Priority = *in.Priority
	}
	for name := range c.Origins {
		if name != "exclude_authors" {
			c.Origins[name] = "need_input"
		}
	}
	if !resolved {
		c.UnverifiedNeedReason = "unresolved_need_constraints"
	}
	if c.UnverifiedNeedReason != "" {
		c.Warnings = append(c.Warnings, c.UnverifiedNeedReason)
		c.SpecHash = hashContext(c)
		return c, nil
	}
	if err = cc.prepareRetrieval(ctx, &c); err != nil {
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
