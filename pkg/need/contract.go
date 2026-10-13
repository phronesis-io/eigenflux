// Package need validates and stores Agent-authored Needs.
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
	InputSchemaVersion = "need_input.v2"
	MaxBodyBytes       = 32 << 10
)

type Target struct {
	Goal    string `json:"goal"`
	Context string `json:"context,omitempty"`
}

type Condition struct {
	Text        string `json:"text"`
	SourceQuote string `json:"source_quote,omitempty"`
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
// action. Its field values are preserved as the authoritative input snapshot.
type Input struct {
	SchemaVersion string      `json:"schema_version"`
	IntentID      int64       `json:"intent_id,string"`
	IntentVersion int64       `json:"intent_version"`
	NeedType      string      `json:"need_type"`
	Target        Target      `json:"target"`
	Priority      *float64    `json:"priority,omitempty"`
	Requirements  []Condition `json:"requirements,omitempty"`
	Preferences   []Condition `json:"preferences,omitempty"`
	Constraints   Constraints `json:"constraints,omitempty"`
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
	if err := decodeJSON(raw, &in); err != nil {
		return in, err
	}
	return in, Validate(in)
}

func decodeJSON(raw []byte, out any) error {
	if len(raw) == 0 || len(raw) > MaxBodyBytes || !utf8.Valid(raw) {
		return invalid("body", "invalid_size_or_encoding")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := checkJSON(d); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return invalid("body", "trailing_json")
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return invalid("body", "invalid_fields_or_types")
	}
	var link struct {
		IntentID    string `json:"intent_id"`
		Constraints struct {
			Currency *string `json:"currency"`
		} `json:"constraints"`
	}
	if err := json.Unmarshal(raw, &link); err != nil {
		return invalid("intent_id", "canonical_positive_int64_required")
	}
	id, err := strconv.ParseInt(link.IntentID, 10, 64)
	if err != nil || id <= 0 || link.IntentID != strconv.FormatInt(id, 10) {
		return invalid("intent_id", "canonical_positive_int64_required")
	}
	if currency := link.Constraints.Currency; currency != nil && *currency != "CNY" {
		return invalid("constraints.currency", "unsupported")
	}
	return nil
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
	if !textValid(in.Target.Goal, 200, true) {
		return invalid("target.goal", "required_or_too_long")
	}
	if !textValid(in.Target.Context, 2000, false) {
		return invalid("target.context", "too_long")
	}
	for _, field := range []struct {
		name       string
		conditions []Condition
	}{{"requirements", in.Requirements}, {"preferences", in.Preferences}} {
		if len(field.conditions) > 20 {
			return invalid(field.name, "too_many")
		}
		for _, c := range field.conditions {
			if !textValid(c.Text, 500, true) || !textValid(c.SourceQuote, 1000, false) {
				return invalid(field.name, "invalid_condition")
			}
		}
	}
	if err := validateCommon(in.IntentID, in.IntentVersion, in.NeedType, in.Priority, in.Constraints); err != nil {
		return err
	}
	c := in.Constraints
	for _, value := range c.Lang {
		if _, ok := languageCode(value); !ok {
			return invalid("constraints.lang", "bcp47_code_required")
		}
	}
	for _, value := range c.ProviderRegion {
		if _, ok := regionCode(value); !ok {
			return invalid("constraints.provider_region", "iso_country_code_required")
		}
	}
	return nil
}

func validateCommon(intentID, intentVersion int64, kind string, priority *float64, c Constraints) error {
	if intentID <= 0 || intentVersion <= 0 {
		return invalid("intent", "positive_intent_id_and_version_required")
	}
	if kind != "broadcast" && kind != "commission" && kind != "agent" {
		return invalid("need_type", "unsupported")
	}
	if priority != nil && (math.IsNaN(*priority) || math.IsInf(*priority, 0) || *priority < 0 || *priority > 1) {
		return invalid("priority", "out_of_range")
	}
	if c.BudgetMaxFen != nil || c.Currency != "" || c.MaxDurationMS != nil {
		if kind != "commission" {
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
