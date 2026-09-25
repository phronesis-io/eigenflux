// Package discovery owns the wire and execution contracts shared by the
// forward search pipeline. It has no dependency on a particular RPC transport.
package discovery

import (
	"bytes"
	"eigenflux_server/pkg/need"
	searchindex "eigenflux_server/rpc/sort/discovery/index"
	"encoding/json"
	"fmt"
	"sort"
)

const PipelineVersion = "need_search_v1"

type Kind string

const (
	Broadcast  Kind = "broadcast"
	Commission Kind = "commission"
	Agent      Kind = "agent"
)

type Mode string

const (
	Search         Mode = "search"
	Recommendation Mode = "recommendation"
)

var AllKinds = []Kind{Broadcast, Commission, Agent}

func (k Kind) Valid() bool { return k == Broadcast || k == Commission || k == Agent }

type FieldError struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}
type Error struct {
	Code   int          `json:"code"`
	Reason string       `json:"reason"`
	Fields []FieldError `json:"errors,omitempty"`
}

func (e *Error) Error() string { return e.Reason }
func Invalid(path, reason string) error {
	return &Error{Code: 400, Reason: "invalid_request", Fields: []FieldError{{path, reason}}}
}
func Failure(code int, reason string) error { return &Error{Code: code, Reason: reason} }

type Filters struct {
	Category        string   `json:"category,omitempty"`
	Subtype         string   `json:"subtype,omitempty"`
	Intents         []string `json:"intents,omitempty"`
	TaxonomyVersion string   `json:"taxonomy_version,omitempty"`
	BudgetMaxFen    *int64   `json:"budget_max_fen,omitempty"`
	Currency        string   `json:"currency,omitempty"`
	MinPriceFen     *int64   `json:"min_price_fen,omitempty"`
	MaxPriceFen     *int64   `json:"max_price_fen,omitempty"`
	MinDurationMS   *int64   `json:"min_promised_delivery_ms,omitempty"`
	MaxDurationMS   *int64   `json:"max_promised_delivery_ms,omitempty"`
	DeadlineMS      *int64   `json:"deadline_ms,omitempty"`
	ProviderRegion  []string `json:"provider_region,omitempty"`
	Lang            []string `json:"lang,omitempty"`
	ExcludeTerms    []string `json:"exclude_terms,omitempty"`
	ExcludeAuthors  []string `json:"exclude_authors,omitempty"`
}
type Defaults struct {
	Language       string `json:"language,omitempty"`
	ProviderRegion string `json:"provider_region,omitempty"`
}

// NeedInput uses the same form as the capture API, including its Intent link.
type NeedInput = need.Input

type Request struct {
	agentExact        bool
	InheritedLanguage bool       `json:"-"`
	KindsExplicit     bool       `json:"-"`
	LegacyLimit       bool       `json:"-"`
	LegacyPrefetch    int        `json:"-"`
	Query             string     `json:"query,omitempty"`
	NeedID            int64      `json:"need_id,string,omitempty"`
	Need              *NeedInput `json:"need,omitempty"`
	NeedIDs           []string   `json:"need_ids,omitempty"`
	SourceKinds       []Kind     `json:"source_kinds,omitempty"`
	Filters           Filters    `json:"filters,omitempty"`
	Limit             int        `json:"limit,omitempty"`
	Defaults          Defaults   `json:"defaults,omitempty"`
}

// UnmarshalJSON applies the capture contract to an inline form before typed
// decoding can lose duplicate keys, null restrictions, or noncanonical IDs.
func (r *Request) UnmarshalJSON(raw []byte) error {
	type wire Request
	var value wire
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	if value.Need != nil {
		input, err := need.Decode(fields["need"])
		if err != nil {
			return err
		}
		value.Need = &input
	}
	*r = Request(value)
	return nil
}

type Context struct {
	CapturedNeed       *need.Snapshot    `json:"captured_need,omitempty"`
	QueryAnalysis      *QueryAnalysis    `json:"query_analysis,omitempty"`
	SourceNeedID       int64             `json:"source_need_id,string,omitempty"`
	SourceNeedRevision int64             `json:"source_need_revision,omitempty"`
	ID                 int64             `json:"context_id,string"`
	OwnerID            int64             `json:"agent_id,string"`
	Revision           int64             `json:"revision"`
	Persistence        string            `json:"persistence"`
	Origin             string            `json:"input_origin"`
	State              string            `json:"state"`
	Query              string            `json:"query,omitempty"`
	Kinds              []Kind            `json:"source_kinds"`
	Filters            Filters           `json:"effective_filters"`
	SoftIntents        []string          `json:"soft_intents,omitempty"`
	Priority           float64           `json:"priority"`
	Origins            map[string]string `json:"field_origins,omitempty"`
	SourceRevision     string            `json:"source_revision,omitempty"`
	TaxonomyVersion    string            `json:"taxonomy_version"`
	CompilerVersion    string            `json:"compiler_version"`
	EmbeddingVersion   string            `json:"embedding_version,omitempty"`
	Vector             []float32         `json:"-"`
	SpecHash           string            `json:"spec_hash"`
	CreatedAt          int64             `json:"created_at"`
	UpdatedAt          int64             `json:"updated_at"`
	ExpiresAt          int64             `json:"expires_at,omitempty"`
	Warnings           []string          `json:"warnings,omitempty"`
}

func (c Context) NeedID() int64 {
	if c.SourceNeedID != 0 {
		return c.SourceNeedID
	}

	return 0
}
func (c Context) Active(now int64) bool {
	return c.State == "active" && (c.Filters.DeadlineMS == nil || *c.Filters.DeadlineMS > now) && (c.ExpiresAt == 0 || c.ExpiresAt > now)
}

type SourceRef struct {
	Type Kind  `json:"type"`
	ID   int64 `json:"id,string"`
}

func (r SourceRef) Key() string { return fmt.Sprintf("%s:%d", r.Type, r.ID) }

type Document struct {
	StatisticsVersion int64             `json:"statistics_version,omitempty"`
	SourceIndex       string            `json:"source_index,omitempty"`
	ProjectionVersion int64             `json:"projection_version,omitempty"`
	Ref               SourceRef         `json:"source_ref"`
	AuthorID          int64             `json:"author_id,string"`
	Version           string            `json:"version"`
	Text              string            `json:"text"`
	Preview           string            `json:"preview"`
	Active            bool              `json:"active"`
	Visible           bool              `json:"visible"`
	Blocked           bool              `json:"blocked"`
	KnownContact      bool              `json:"known_contact"`
	GroupID           int64             `json:"group_id,omitempty"`
	Slots             searchindex.Slots `json:"slots"`
	PriceFen          *int64            `json:"price_fen,omitempty"`
	Currency          string            `json:"currency,omitempty"`
	DurationMS        *int64            `json:"duration_ms,omitempty"`
	ExpiresAt         int64             `json:"expires_at,omitempty"`
	SourceUpdatedAt   int64             `json:"source_updated_at,omitempty"`
	FreshAt           int64             `json:"fresh_at,omitempty"`
	ActivityAt        int64             `json:"activity_at,omitempty"`
	Quality           float64           `json:"quality"`
	Fulfillment       float64           `json:"fulfillment"`
	Vector            []float32         `json:"-"`
	Lexical           float64           `json:"lexical"`
	Channels          []string          `json:"channels"`
	ExactMatch        string            `json:"exact_match,omitempty"`
	ContentType       string            `json:"content_type,omitempty"`
	SourceType        string            `json:"source_type,omitempty"`
	URL               string            `json:"url,omitempty"`
}
type Score struct {
	Threshold     float64            `json:"threshold"`
	MinRelevance  float64            `json:"min_relevance"`
	RequestTime   int64              `json:"request_time"`
	Contributions map[string]float64 `json:"contributions"`
	Value         float64            `json:"value"`
	Relevance     float64            `json:"relevance"`
	Eligible      bool               `json:"eligible"`
	ScorerType    string             `json:"scorer_type"`
	Version       string             `json:"scorer_version"`
	Kind          string             `json:"score_kind"`
	ConfigHash    string             `json:"config_hash"`
	Features      map[string]float64 `json:"features"`
	Missing       []string           `json:"missing,omitempty"`
}
type Candidate struct {
	FinalScore      float64  `json:"final_score"`
	Order           int      `json:"policy_order,omitempty"`
	ClaimTTLSeconds int64    `json:"claim_ttl_seconds,omitempty"`
	Context         Context  `json:"context"`
	Document        Document `json:"document"`
	Score           Score    `json:"score"`
	Reasons         []string `json:"policy_reasons,omitempty"`
}
type ResultItem struct {
	Ref          SourceRef         `json:"source_ref"`
	ItemID       int64             `json:"item_id,string,omitempty"`
	ContextID    int64             `json:"context_id,string"`
	NeedID       int64             `json:"need_id,string,omitempty"`
	NeedRevision int64             `json:"need_revision,omitempty"`
	Preview      map[string]string `json:"preview"`
	Match        map[string]any    `json:"match"`
}
type Response struct {
	RequestID        string       `json:"request_id"`
	ImpressionID     string       `json:"impression_id"`
	Mode             Mode         `json:"mode"`
	PipelineVersion  string       `json:"pipeline_version"`
	Origin           string       `json:"input_origin"`
	ContextID        int64        `json:"context_id,string,omitempty"`
	Items            []ResultItem `json:"items"`
	Status           string       `json:"result_status"`
	Partial          bool         `json:"partial"`
	FallbackReason   string       `json:"fallback_reason,omitempty"`
	Reasons          []string     `json:"partial_reasons,omitempty"`
	EffectiveFilters Filters      `json:"effective_filters"`
	ConstraintMode   string       `json:"constraint_mode"`
	HasMore          bool         `json:"has_more"`
}

func PublicItem(c Candidate) ResultItem {
	r := ResultItem{Ref: c.Document.Ref, ContextID: c.Context.ID, Preview: map[string]string{"text": c.Document.Preview}, Match: map[string]any{"score": c.Score.Value, "scorer_type": c.Score.ScorerType, "scorer_version": c.Score.Version, "score_kind": c.Score.Kind}}
	if c.Document.ExactMatch != "" {
		r.Match["exact"] = c.Document.ExactMatch
	}
	if c.Document.Ref.Type == Broadcast {
		r.ItemID = c.Document.Ref.ID
	}
	if c.Context.NeedID() != 0 {
		r.NeedID = c.Context.NeedID()
		r.NeedRevision = c.Context.Revision
		if c.Context.SourceNeedID != 0 {
			r.NeedRevision = c.Context.SourceNeedRevision
		}
	}
	fields := []string{}
	for name, present := range map[string]bool{"category": c.Context.Filters.Category != "", "subtype": c.Context.Filters.Subtype != "", "language": len(c.Context.Filters.Lang) > 0, "provider_region": len(c.Context.Filters.ProviderRegion) > 0, "intents": len(c.Context.Filters.Intents) > 0, "budget": c.Context.Filters.BudgetMaxFen != nil, "deadline": c.Context.Filters.DeadlineMS != nil} {
		if present {
			fields = append(fields, name)
		}
	}
	sort.Strings(fields)
	r.Match["fields"] = fields
	r.Match["match_types"] = matchTypes(c.Document.Channels)
	return r
}

// PublicResponse removes numeric ranking diagnostics from unified HTTP and
// legacy Feed metadata. The internal envelope retains them for the existing
// commission score DTO, whose compatibility contract already exposes a score.
func PublicResponse(r Response) Response {
	items := make([]ResultItem, len(r.Items))
	for i, it := range r.Items {
		items[i] = it
		fields := map[string]any{}
		for k, v := range it.Match {
			if k != "score" {
				fields[k] = v
			}
		}
		items[i].Match = fields
	}
	r.Items = items
	return r
}
