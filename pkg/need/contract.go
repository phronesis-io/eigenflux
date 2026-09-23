// Package need owns the Agent-authored NeedInput contract and the platform's
// derived NormalizedNeed projection. Intent text remains in agent_intent_actions.
package need

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"eigenflux_server/pkg/validator"
)

const (
	InputSchemaVersion      = "need_input.v1"
	NormalizedSchemaVersion = "normalized_need.v1"
	MaxBodyBytes            = 32 << 10
)

type Target struct {
	Desc           string   `json:"desc"`
	CandidateNeeds []string `json:"candidate_needs"`
}

type Constraints struct {
	BudgetMaxFen   *int64   `json:"budget_max_fen,omitempty"`
	Currency       string   `json:"currency,omitempty"`
	MaxDurationMS  *int64   `json:"max_promised_delivery_ms,omitempty"`
	DeadlineMS     *int64   `json:"deadline_ms,omitempty"`
	ProviderRegion []string `json:"provider_region,omitempty"`
	Lang           []string `json:"lang,omitempty"`
	ExcludeTerms   []string `json:"exclude_terms,omitempty"`
}

// Input is the Agent's structured interpretation of one confirmed intent
// action. Its field values are preserved and never overwritten by normalization.
type Input struct {
	SchemaVersion string      `json:"schema_version"`
	IntentID      int64       `json:"intent_id,string"`
	IntentVersion int64       `json:"intent_version"`
	NeedType      string      `json:"need_type"`
	Target        Target      `json:"target"`
	Priority      *float64    `json:"priority,omitempty"`
	Preferences   string      `json:"preferences,omitempty"`
	Constraints   Constraints `json:"constraints,omitempty"`
}

// Normalized contains only derived content. Source kind, priority and preferences
// remain in Input; schema and coverage metadata live on the projection record.
// Historical projections retain these derived values for offline evaluation.
type Normalized struct {
	Desc                  string                `json:"desc"`
	CandidateNeeds        []string              `json:"candidate_needs"`
	MappedNeeds           map[string]string     `json:"mapped_needs,omitempty"`
	Constraints           Constraints           `json:"constraints,omitempty"`
	UnresolvedConstraints UnresolvedConstraints `json:"unresolved_constraints,omitempty"`
}

// MappingStatus measures vocabulary coverage, independently of eligibility.
func (n Normalized) MappingStatus() string {
	matched := 0
	for _, phrase := range n.CandidateNeeds {
		if n.MappedNeeds[phrase] != "" {
			matched++
		}
	}
	if matched == 0 {
		return MappingUnmapped
	}
	if matched == len(n.CandidateNeeds) {
		return MappingMapped
	}
	return MappingPartial
}

// Unresolved constraints retain explicit restrictions that cannot safely become
// canonical filters. Consumers must retain them for contextual matching.
type UnresolvedConstraints struct {
	Lang           []string `json:"lang,omitempty"`
	ProviderRegion []string `json:"provider_region,omitempty"`
}

type FieldError struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

func (e *FieldError) Error() string     { return e.Path + ": " + e.Reason }
func invalid(path, reason string) error { return &FieldError{Path: path, Reason: reason} }

// Decode validates one complete JSON object, rejects duplicate/unknown fields,
// and leaves all submitted text unchanged in the decoded strings.
func Decode(raw []byte) (Input, error) {
	var in Input
	if len(raw) == 0 || len(raw) > MaxBodyBytes || !utf8.Valid(raw) {
		return in, invalid("body", "invalid_size_or_encoding")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := checkJSON(d); err != nil {
		return in, err
	}
	if _, err := d.Token(); err != io.EOF {
		return in, invalid("body", "trailing_json")
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&in); err != nil {
		return in, invalid("body", "invalid_fields_or_types")
	}
	var link struct {
		IntentID    string `json:"intent_id"`
		Constraints struct {
			Currency *string `json:"currency"`
		} `json:"constraints"`
	}
	if err := json.Unmarshal(raw, &link); err != nil || link.IntentID != strconv.FormatInt(in.IntentID, 10) {
		return in, invalid("intent_id", "canonical_positive_int64_required")
	}
	if currency := link.Constraints.Currency; currency != nil && *currency != "CNY" {
		return in, invalid("constraints.currency", "unsupported")
	}
	return in, Validate(in)
}

func checkJSON(d *json.Decoder) error {
	t, err := d.Token()
	if err != nil || t == nil {
		return invalid("body", "invalid_json_or_null")
	}
	if delim, ok := t.(json.Delim); ok {
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return invalid("body", "invalid_json")
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return invalid("body", "duplicate_field")
				}
				seen[name] = true
				if err := checkJSON(d); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err := checkJSON(d); err != nil {
					return err
				}
			}
		default:
			return invalid("body", "invalid_json")
		}
		if _, err := d.Token(); err != nil {
			return invalid("body", "invalid_json")
		}
	}
	return nil
}

func textValid(s string, max int, required bool) bool {
	return utf8.ValidString(s) && !strings.ContainsRune(s, 0) &&
		(!required || strings.TrimSpace(s) != "") && validator.CalculateMultilingualLength(s) <= max
}

func Validate(in Input) error {
	if in.SchemaVersion != InputSchemaVersion {
		return invalid("schema_version", "unsupported")
	}
	if in.IntentID <= 0 || in.IntentVersion <= 0 {
		return invalid("intent", "positive_intent_id_and_version_required")
	}
	if in.NeedType != "broadcast" && in.NeedType != "commission" && in.NeedType != "agent" {
		return invalid("need_type", "unsupported")
	}
	if !textValid(in.Target.Desc, 200, true) {
		return invalid("target.desc", "required_or_too_long")
	}
	if !textValid(in.Preferences, 500, false) {
		return invalid("preferences", "too_long")
	}
	if len(in.Target.CandidateNeeds) < 1 || len(in.Target.CandidateNeeds) > 10 {
		return invalid("target.candidate_needs", "require_1_to_10")
	}
	for _, phrase := range in.Target.CandidateNeeds {
		if !textValid(phrase, 200, true) {
			return invalid("target.candidate_needs", "invalid_phrase")
		}
	}
	if in.Priority != nil && (math.IsNaN(*in.Priority) || math.IsInf(*in.Priority, 0) || *in.Priority < 0 || *in.Priority > 1) {
		return invalid("priority", "out_of_range")
	}
	c := in.Constraints
	if c.BudgetMaxFen != nil || c.Currency != "" || c.MaxDurationMS != nil {
		if in.NeedType != "commission" {
			return invalid("constraints", "commission_only_price_and_delivery")
		}
	}
	if c.BudgetMaxFen != nil && (*c.BudgetMaxFen < 0 || c.Currency == "") {
		return invalid("constraints.budget_max_fen", "nonnegative_amount_and_currency_required")
	}
	if c.Currency != "" && c.Currency != "CNY" {
		return invalid("constraints.currency", "unsupported")
	}
	if c.MaxDurationMS != nil && *c.MaxDurationMS < 0 {
		return invalid("constraints.max_promised_delivery_ms", "negative")
	}
	if c.DeadlineMS != nil && *c.DeadlineMS <= 0 {
		return invalid("constraints.deadline_ms", "positive_timestamp_required")
	}
	for _, field := range []struct {
		name   string
		values []string
	}{{"lang", c.Lang}, {"provider_region", c.ProviderRegion}, {"exclude_terms", c.ExcludeTerms}} {
		if len(field.values) > 20 {
			return invalid("constraints."+field.name, "too_many")
		}
		for _, value := range field.values {
			if !textValid(value, 100, true) {
				return invalid("constraints."+field.name, "invalid_value")
			}

		}
	}
	return nil
}
