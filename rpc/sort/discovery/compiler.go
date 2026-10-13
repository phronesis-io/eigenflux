package discovery

import (
	"bytes"
	"context"
	"crypto/sha256"
	"eigenflux_server/pkg/cache"
	"eigenflux_server/pkg/need"
	"eigenflux_server/rpc/sort/discovery/needembedding"
	"eigenflux_server/rpc/sort/discovery/queryprocessing"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"eigenflux_server/pkg/validator"
)

const onlineEmbeddingTimeout = 2 * time.Second

// Intent fields each allow 1,000 Unicode runes, plus their joining space.
const agentContextMaxRunes = 2001

type Embedder interface {
	GetEmbedding(context.Context, string) ([]float32, error)
}
type NeedVectorLookup interface {
	Lookup(context.Context, int64, string, string) ([]float32, error)
}
type Compiler struct {
	Cache            *cache.DiscoveryCache
	NeedVectors      NeedVectorLookup
	Embedder         Embedder
	EmbeddingVersion string
}

func Decode[T any](raw []byte) (T, error) {
	var out T
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&out); err != nil {
		return out, Invalid("body", "invalid_json")
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return out, Invalid("body", "trailing_json")
	}
	return out, nil
}
func textOK(s string, max int) bool {
	return strings.TrimSpace(s) != "" && validator.CalculateMultilingualLength(s) <= max
}
func ValidateFilters(f Filters, kinds []Kind, now int64) error {
	numeric := f.BudgetMaxFen != nil || f.MinPriceFen != nil || f.MaxPriceFen != nil || f.MinDurationMS != nil || f.MaxDurationMS != nil || f.Currency != ""
	if numeric && (len(kinds) != 1 || kinds[0] != Commission) {
		return Invalid("filters", "commission_only")
	}
	for _, p := range []*int64{f.BudgetMaxFen, f.MinPriceFen, f.MaxPriceFen, f.MinDurationMS, f.MaxDurationMS} {
		if p != nil && *p < 0 {
			return Invalid("filters", "negative_bound")
		}
	}
	for _, p := range [][2]*int64{{f.MinPriceFen, f.MaxPriceFen}, {f.MinDurationMS, f.MaxDurationMS}, {f.MinPriceFen, f.BudgetMaxFen}} {
		if p[0] != nil && p[1] != nil && *p[0] > *p[1] {
			return Invalid("filters", "conflicting_bounds")
		}
	}
	if (f.BudgetMaxFen != nil || f.MinPriceFen != nil || f.MaxPriceFen != nil) && f.Currency == "" {
		return Invalid("filters.currency", "required_with_price")
	}
	if f.Currency != "" && f.Currency != "CNY" && f.Currency != "USD" {
		return Invalid("filters.currency", "unsupported_currency")
	}
	if f.DeadlineMS != nil && *f.DeadlineMS <= now {
		return Invalid("filters.deadline_ms", "elapsed")
	}
	for path, values := range map[string][]string{"lang": f.Lang, "provider_region": f.ProviderRegion, "exclude_terms": f.ExcludeTerms} {
		if len(values) > 20 {
			return Invalid("filters."+path, "too_many")
		}
		for _, v := range values {
			if !textOK(v, 100) {
				return Invalid("filters."+path, "invalid_value")
			}
		}
	}
	if len(f.ExcludeAuthors) > 20 {
		return Invalid("filters.exclude_authors", "too_many")
	}
	for _, id := range f.ExcludeAuthors {
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil || n <= 0 {
			return Invalid("filters.exclude_authors", "invalid_id")
		}
	}
	return nil
}
func NormalizeRequest(r Request, mode Mode, now int64) (Request, error) {
	if mode != Search && mode != Recommendation {
		return r, Invalid("mode", "unsupported")
	}
	r.KindsExplicit = len(r.SourceKinds) > 0
	if r.CommissionID < 0 {
		return r, Invalid("commission_id", "invalid_id")
	}
	if r.CommissionID > 0 {
		if mode != Search || len(r.SourceKinds) > 0 && (len(r.SourceKinds) != 1 || r.SourceKinds[0] != Commission) {
			return r, Invalid("commission_id", "commission_search_only")
		}
		r.SourceKinds = []Kind{Commission}
	}
	if r.Defaults.ProviderRegion != "" && r.Defaults.ProviderRegion != "none" {
		return r, Invalid("defaults.provider_region", "unsupported_inheritance")
	}
	if r.Defaults.Language != "" && r.Defaults.Language != "none" && r.Defaults.Language != "card" {
		return r, Invalid("defaults.language", "invalid")
	}
	if len(r.SourceKinds) == 0 {
		r.SourceKinds = append([]Kind(nil), AllKinds...)
	}
	seen := map[Kind]bool{}
	for _, k := range r.SourceKinds {
		if !k.Valid() || seen[k] {
			return r, Invalid("source_kinds", "invalid_or_duplicate")
		}
		seen[k] = true
	}
	if mode == Search {
		n := 0
		if strings.TrimSpace(r.Query) != "" {
			n++
		}
		if r.NeedID < 0 {
			return r, Invalid("need_id", "invalid_id")
		}
		if r.NeedID != 0 {
			n++
		}
		if r.Need != nil {
			n++
		}
		if r.CommissionID > 0 {
			n++
		}
		if n != 1 || len(r.NeedIDs) > 0 {
			return r, Invalid("body", "exactly_one_search_input")
		}
		if r.Query != "" && !textOK(r.Query, 2000) {
			return r, Invalid("query", "invalid_length")
		}
		if r.Limit == 0 {
			r.Limit = 20
		}
		maxLimit := 50
		if r.LegacyLimit {
			maxLimit = 100
		}
		if r.Limit < 1 || r.Limit > maxLimit {
			return r, Invalid("limit", "out_of_range")
		}
		if r.Need != nil || r.NeedID != 0 {
			if r.Defaults.Language != "" || r.Defaults.ProviderRegion != "" {
				return r, Invalid("defaults", "use_need_defaults")
			}
			b, _ := json.Marshal(r.Filters)
			if string(b) != "{}" {
				return r, Invalid("filters", "use_need_constraints")
			}
		}
	} else {
		if r.Query != "" || r.Need != nil || r.NeedID != 0 {
			return r, Invalid("body", "automatic_input_conflict")
		}
		if r.Cursor != "" {
			return r, Invalid("cursor", "search_only")
		}
		if r.Limit == 0 {
			r.Limit = 20
		}
		if r.Limit < 1 || r.Limit > 100 {
			return r, Invalid("limit", "out_of_range")
		}
		if len(r.NeedIDs) > 5 {
			return r, Invalid("need_ids", "too_many")
		}
		for _, id := range r.NeedIDs {
			n, e := strconv.ParseInt(id, 10, 64)
			if e != nil || n <= 0 {
				return r, Invalid("need_ids", "invalid_id")
			}
		}
	}
	if err := ValidateFilters(r.Filters, r.SourceKinds, now); err != nil {
		return r, err
	}
	return r, nil
}
func compileBase(owner int64, origin, query string, kinds []Kind, f Filters, identity bool) CompiledContext {
	f.ExcludeAuthors = append(append([]string(nil), f.ExcludeAuthors...), strconv.FormatInt(owner, 10))
	origins := map[string]string{"exclude_authors": "system"}
	raw, _ := json.Marshal(f)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	for k := range fields {
		if k != "exclude_authors" {
			origins[k] = "explicit"
		}
	}
	c := CompiledContext{Origin: origin, Query: strings.TrimSpace(query), Kinds: kinds, Filters: f, Origins: origins, CompilerVersion: contextCompilerVersion}
	if c.Query != "" {
		c.QueryAnalysis = queryprocessing.Process(c.Query, queryprocessing.Options{Identity: identity})
	}
	return c
}

func hashContext(c Context) string {
	b, _ := json.Marshal(struct {
		Query               string
		Kinds               []Kind
		Filters             Filters
		Captured            *need.Snapshot
		QueryAnalysis       *queryprocessing.Analysis
		Embedding, Compiler string
	}{c.Query, c.Kinds, c.Filters, c.CapturedNeed, c.QueryAnalysis, c.EmbeddingVersion, c.CompilerVersion})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (cc *Compiler) embed(ctx context.Context, c *Context) error {
	if c.Query == "" || c.Origin == "agent_context" || c.Origin == "baseline" {
		c.SpecHash = hashContext(*c)
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	work, cancel := context.WithTimeout(ctx, onlineEmbeddingTimeout)
	defer cancel()
	if c.CapturedNeed != nil && c.CapturedNeed.InputID > 0 && cc.NeedVectors != nil {
		v, err := cc.NeedVectors.Lookup(work, c.CapturedNeed.InputID, c.lexicalQuery(), c.QueryAnalysis.Version)
		if errors.Is(err, needembedding.ErrPending) {
			c.Warnings = append(c.Warnings, "embedding_pending")
		} else if err != nil || len(v) == 0 {
			c.Warnings = append(c.Warnings, "embedding_unavailable")
		} else {
			c.Vector = v
		}
	} else if cc.Embedder == nil {
		c.Warnings = append(c.Warnings, "embedding_unavailable")
	} else {
		v, err := cc.Embedder.GetEmbedding(work, c.lexicalQuery())
		if err != nil || len(v) == 0 {
			c.Warnings = append(c.Warnings, "embedding_unavailable")
		} else {
			for _, x := range v {
				if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
					return Failure(503, "invalid_embedding")
				}
			}
			c.Vector = v
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	c.SpecHash = hashContext(*c)
	return nil
}
func (cc *Compiler) Query(ctx context.Context, owner, id, now int64, r Request, origin string) (Context, error) {
	if r.CommissionID > 0 {
		r.Query = strconv.FormatInt(r.CommissionID, 10)
	}
	if origin != "query" && origin != "agent_context" && origin != "baseline" {
		return Context{}, Invalid("input_origin", "unsupported")
	}
	truncated := false
	if origin == "agent_context" {
		r.Query = strings.TrimSpace(r.Query)
		if utf8.RuneCountInString(r.Query) > agentContextMaxRunes {
			r.Query = string([]rune(r.Query)[:agentContextMaxRunes])
			truncated = true
		}
	}
	if origin == "agent_context" && r.Query == "" || origin == "query" && !textOK(r.Query, 2000) {
		return Context{}, Invalid("query", "invalid_length")
	}
	plan, err := cc.compiled(ctx, owner, now, struct {
		Origin, Query, SourceRevision string
		Kinds                         []Kind
		Filters                       Filters
		Identity, InheritedLanguage   bool
	}{origin, r.Query, r.SourceRevision, r.SourceKinds, r.Filters, r.agentExact || decimalAgentQuery(r.Query), r.InheritedLanguage}, func() (CompiledContext, error) {
		p := compileBase(owner, origin, r.Query, r.SourceKinds, r.Filters, origin == "query" && (r.agentExact || decimalAgentQuery(r.Query)))
		p.SourceRevision = r.SourceRevision
		if r.InheritedLanguage {
			p.Origins["lang"] = "card_default"
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
	if truncated {
		c.Warnings = append(c.Warnings, "context_query_truncated")
	}
	if origin == "query" && (r.CommissionID > 0 || len(c.Kinds) == 1 && c.Kinds[0] == Agent && (r.agentExact || decimalAgentQuery(c.Query))) {
		c.SpecHash = hashContext(c)
		return c, nil
	}
	if err = cc.embed(ctx, &c); err != nil {
		return c, err
	}
	return c, nil
}
