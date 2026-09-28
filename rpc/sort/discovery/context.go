package discovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"eigenflux_server/pkg/need"
	"eigenflux_server/rpc/sort/discovery/queryprocessing"
)

const contextCompilerVersion = "context_rules_v6"
const needCompilerVersion = "need_input_context_v5"

// CompiledContext is an immutable retrieval value. It contains no execution ID,
// request clock, vector or runtime warning. Context is its per-execution wire
// snapshot, retained in the existing sample and frozen-page contracts.
type CompiledContext struct {
	Query                string
	Kinds                []Kind
	Filters              Filters
	Origins              map[string]string
	Origin               string
	Priority             float64
	SourceRevision       string
	CapturedNeed         *need.Snapshot
	UnverifiedNeedReason string
	CompilerVersion      string
	QueryAnalysis        *queryprocessing.Analysis
}

func contextDigest(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (cc *Compiler) compiled(ctx context.Context, owner, now int64, input any, build func() (CompiledContext, error)) (CompiledContext, error) {
	var out CompiledContext
	key := contextDigest(struct {
		QueryVersion, ContextVersion, NeedVersion string
		Input                                     any
	}{queryprocessing.Version, contextCompilerVersion, needCompilerVersion, input})
	err := cc.Cache.Load(ctx, owner, "compiled", key, now, 15*time.Minute, &out, func(context.Context) (any, int64, error) {
		c, err := build()
		return c, 0, err
	})
	return out, err
}

func (p CompiledContext) execution(owner, id, now int64, embeddingVersion string) (Context, error) {
	if owner <= 0 || id <= 0 {
		return Context{}, Invalid("identity", "invalid")
	}
	if p.CapturedNeed != nil && p.Filters.DeadlineMS != nil && *p.Filters.DeadlineMS <= now {
		return Context{}, Failure(409, "expired_need")
	}
	validation := p.Filters
	// The final author exclusion is server-added, outside the caller's limit.
	if n := len(validation.ExcludeAuthors); n > 0 {
		validation.ExcludeAuthors = validation.ExcludeAuthors[:n-1]
	}
	if err := ValidateFilters(validation, p.Kinds, now); err != nil {
		return Context{}, err
	}
	c := Context{ID: id, OwnerID: owner, Revision: 1, Persistence: "ephemeral", Origin: p.Origin, State: "active",
		Query: p.Query, Kinds: p.Kinds, Filters: p.Filters, Origins: p.Origins, Priority: p.Priority,
		SourceRevision: p.SourceRevision, CapturedNeed: p.CapturedNeed, UnverifiedNeedReason: p.UnverifiedNeedReason,
		CompilerVersion: p.CompilerVersion, QueryAnalysis: p.QueryAnalysis, EmbeddingVersion: embeddingVersion,
		CreatedAt: now, UpdatedAt: now, ExpiresAt: now + int64(30*24*time.Hour/time.Millisecond)}
	if p.CapturedNeed != nil {
		c.SourceNeedID, c.SourceNeedRevision = p.CapturedNeed.InputID, p.CapturedNeed.IntentVersion
	}
	return c, nil
}
