package discovery

import (
	"context"
	"errors"

	"eigenflux_server/pkg/need"
	"eigenflux_server/rpc/sort/discovery/queryprocessing"
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
	plan, err := cc.compiled(ctx, owner, now, snapshot, func() (CompiledContext, error) {
		in, err := snapshot.ExecutionInput()
		if err != nil {
			return CompiledContext{}, Invalid("need", err.Error())
		}
		constraints, resolved := need.ExecutionConstraints(in.Constraints)
		f := Filters{BudgetMaxFen: constraints.BudgetMaxFen, Currency: constraints.Currency,
			MaxDurationMS: constraints.MaxDurationMS, DeadlineMS: constraints.DeadlineMS,
			ProviderRegion: constraints.ProviderRegion, Lang: constraints.Lang, ExcludeTerms: constraints.ExcludeTerms}
		origin := "need_input"
		if snapshot.InputID == 0 {
			origin = "inline_need"
		}
		p := compileBase(owner, origin, queryprocessing.NeedText(in.Target.Goal, in.Target.Context), []Kind{Kind(in.NeedType)}, f, false)
		p.CompilerVersion = needCompilerVersion
		p.CapturedNeed = &snapshot
		if in.Priority != nil {
			p.Priority = *in.Priority
		}
		for name := range p.Origins {
			if name != "exclude_authors" {
				p.Origins[name] = "need_input"
			}
		}
		if !resolved {
			p.UnverifiedNeedReason = "unresolved_need_constraints"
		}
		return p, nil
	})
	if err != nil {
		return Context{}, err
	}
	c, err := plan.execution(owner, id, now, cc.EmbeddingVersion)
	if err != nil {
		return c, err
	}
	if c.UnverifiedNeedReason != "" {
		c.Warnings = append(c.Warnings, c.UnverifiedNeedReason)
		c.SpecHash = hashContext(c)
		return c, nil
	}
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
