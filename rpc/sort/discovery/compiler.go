package discovery

import (
	"bytes"
	"context"
	"crypto/sha256"
	"eigenflux_server/pkg/metrics"
	"eigenflux_server/pkg/need"
	searchindex "eigenflux_server/rpc/sort/discovery/index"
	"eigenflux_server/rpc/sort/discovery/queryprocessing"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"eigenflux_server/pkg/validator"
)

type Embedder interface {
	GetEmbedding(context.Context, string) ([]float32, error)
}
type Compiler struct {
	Taxonomy         *searchindex.Vocabulary
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
	if len(f.Intents) > 0 && f.TaxonomyVersion == "" {
		return Invalid("filters.taxonomy_version", "required_with_intents")
	}
	if f.Subtype != "" && f.Category == "" {
		return Invalid("filters.subtype", "category_required")
	}
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
	for path, values := range map[string][]string{"lang": f.Lang, "provider_region": f.ProviderRegion, "exclude_terms": f.ExcludeTerms, "intents": f.Intents} {
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
func (cc *Compiler) compileBase(owner, id, now int64, origin, query string, kinds []Kind, f Filters, identity bool) (Context, error) {
	if owner <= 0 || id <= 0 {
		return Context{}, Invalid("identity", "invalid")
	}
	if cc.Taxonomy == nil {
		return Context{}, Failure(503, "taxonomy_unavailable")
	}
	if err := ValidateFilters(f, kinds, now); err != nil {
		return Context{}, err
	}
	if f.Category != "" && !cc.Taxonomy.ValidBranch(f.Category, f.Subtype) {
		return Context{}, Invalid("filters.category", "unknown_taxonomy_branch")
	}
	if f.TaxonomyVersion != "" && f.TaxonomyVersion != cc.Taxonomy.Version {
		return Context{}, Invalid("filters.taxonomy_version", "stale_taxonomy")
	}
	for _, intent := range f.Intents {
		if !cc.Taxonomy.Intent(intent, f.Category, f.Subtype) {
			return Context{}, Invalid("filters.intents", "invalid_parent")
		}
	}
	if f.Category != "" || len(f.Intents) > 0 {
		f.TaxonomyVersion = cc.Taxonomy.Version
	}
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
	c := Context{ID: id, OwnerID: owner, Revision: 1, Persistence: "ephemeral", Origin: origin, State: "active", Query: strings.TrimSpace(query), Kinds: kinds, Filters: f, Origins: origins, TaxonomyVersion: cc.Taxonomy.Version, CompilerVersion: "context_rules_v3", EmbeddingVersion: cc.EmbeddingVersion, CreatedAt: now, UpdatedAt: now, ExpiresAt: now + int64(30*24*time.Hour/time.Millisecond)}
	if c.Query != "" {
		c.QueryAnalysis = queryprocessing.Process(c.Query, cc.Taxonomy, queryprocessing.Options{Category: f.Category, Subtype: f.Subtype, Identity: identity})
		c.SoftIntents = append([]string(nil), c.QueryAnalysis.Intents...)
	}
	return c, nil
}
func hashContext(c Context) string {
	b, _ := json.Marshal(struct {
		Query                         string
		Kinds                         []Kind
		Filters                       Filters
		Captured                      *need.Snapshot
		QueryAnalysis                 *queryprocessing.Analysis
		Taxonomy, Embedding, Compiler string
	}{c.Query, c.Kinds, c.Filters, c.CapturedNeed, c.QueryAnalysis, c.TaxonomyVersion, c.EmbeddingVersion, c.CompilerVersion})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (cc *Compiler) embed(ctx context.Context, c *Context) error {
	if c.Query == "" {
		c.SpecHash = hashContext(*c)
		return nil
	}
	if cc.Embedder == nil {
		c.Warnings = append(c.Warnings, "embedding_unavailable")
	} else {
		v, err := cc.Embedder.GetEmbedding(ctx, c.lexicalQuery())
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
	c.SpecHash = hashContext(*c)
	return nil
}
func (cc *Compiler) Query(ctx context.Context, owner, id, now int64, r Request, origin string) (Context, error) {
	if origin != "query" && origin != "agent_context" && origin != "baseline" {
		return Context{}, Invalid("input_origin", "unsupported")
	}
	if origin != "baseline" && !textOK(r.Query, 2000) {
		return Context{}, Invalid("query", "invalid_length")
	}
	c, err := cc.compileBase(owner, id, now, origin, r.Query, r.SourceKinds, r.Filters, origin == "query" && (r.agentExact || decimalAgentQuery(r.Query)))
	if err != nil {
		return c, err
	}
	if r.InheritedLanguage {
		c.Origins["lang"] = "card_default"
	}
	if origin == "query" && len(c.Kinds) == 1 && c.Kinds[0] == Agent && (r.agentExact || decimalAgentQuery(c.Query)) {
		c.SpecHash = hashContext(c)
		return c, nil
	}
	if err = cc.prepareRetrieval(ctx, &c); err != nil {
		return c, err
	}
	return c, nil
}

// Both Need and query adapters complete the same analyzed retrieval context.
func (cc *Compiler) prepareRetrieval(ctx context.Context, c *Context) error {
	if err := cc.embed(ctx, c); err != nil {
		return err
	}
	// Ambiguous aliases cannot re-enter through exact taxonomy matching.
	if c.QueryAnalysis != nil && len(c.QueryAnalysis.Ambiguous) == 0 && !c.QueryAnalysis.Identity {
		for _, m := range cc.Taxonomy.Search(c.lexicalQuery(), c.Filters.Category, c.Filters.Subtype, c.Vector, .80, 5) {
			if len(c.SoftIntents) < 5 {
				c.SoftIntents = appendUnique(c.SoftIntents, m.ID)
			}
		}
	}
	if len(c.SoftIntents) == 0 && c.Origin != "baseline" {
		metrics.DiscoveryTaxonomyMisses.WithLabelValues(c.Origin).Inc()
	}
	return nil
}
