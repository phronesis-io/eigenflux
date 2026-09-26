package need

import (
	"encoding/json"
)

type legacyTarget struct {
	Desc           string   `json:"desc"`
	CandidateNeeds []string `json:"candidate_needs"`
}
type legacyInput struct {
	SchemaVersion string       `json:"schema_version"`
	IntentID      int64        `json:"intent_id,string"`
	IntentVersion int64        `json:"intent_version"`
	NeedType      string       `json:"need_type"`
	Target        legacyTarget `json:"target"`
	Priority      *float64     `json:"priority,omitempty"`
	Preferences   string       `json:"preferences,omitempty"`
	Constraints   Constraints  `json:"constraints,omitempty"`
}

type inputLink struct {
	SchemaVersion string `json:"schema_version"`
	IntentID      int64  `json:"intent_id,string"`
	IntentVersion int64  `json:"intent_version"`
}

// decodeCompatible validates either wire version and returns only storage
// metadata. The submitted JSON remains authoritative, including legacy fields.
func decodeCompatible(raw []byte) (inputLink, error) {
	var link inputLink
	if len(raw) == 0 || len(raw) > MaxBodyBytes {
		return link, invalid("body", "invalid_size_or_encoding")
	}
	if err := json.Unmarshal(raw, &link); err != nil {
		return link, invalid("body", "invalid_fields_or_types")
	}
	if link.SchemaVersion == "need_input.v1" {
		_, err := decodeLegacy(raw)
		return link, err
	}
	_, err := Decode(raw)
	return link, err
}

func decodeLegacy(raw []byte) (legacyInput, error) {
	var in legacyInput
	if err := decodeJSON(raw, &in); err != nil {
		return in, err
	}
	return in, validateLegacy(in)
}

func validateLegacy(in legacyInput) error {
	if in.SchemaVersion != "need_input.v1" {
		return invalid("schema_version", "unsupported")
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
	return validateCommon(in.IntentID, in.IntentVersion, in.NeedType, in.Priority, in.Constraints)
}
