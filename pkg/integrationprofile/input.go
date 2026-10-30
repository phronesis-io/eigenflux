// Package integrationprofile normalizes versioned enterprise context without
// making private input part of an Agent's public description.
package integrationprofile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"eigenflux_server/pkg/agentcard"
)

type Input struct {
	SourceRevision int64                      `json:"source_revision"`
	Summary        *string                    `json:"summary,omitempty"`
	Fields         map[string]json.RawMessage `json:"fields,omitempty"`
}

type Normalized struct {
	SourceRevision int64                      `json:"source_revision"`
	Summary        string                     `json:"summary,omitempty"`
	Fields         map[string]json.RawMessage `json:"fields,omitempty"`
	ContentHash    string                     `json:"content_hash"`
}

// Normalize keeps summary text private. Only explicitly provided editable fields
// can change Card facts, and their existing visibility/validation rules apply.
func Normalize(in Input) (Normalized, error) {
	out := Normalized{SourceRevision: in.SourceRevision}
	if in.SourceRevision < 1 {
		return out, fmt.Errorf("source_revision must be positive")
	}
	if (in.Summary == nil) == (in.Fields == nil) {
		return out, fmt.Errorf("provide exactly one of summary or fields")
	}
	if in.Summary != nil {
		if !utf8.ValidString(*in.Summary) || utf8.RuneCountInString(*in.Summary) > 4000 {
			return out, fmt.Errorf("summary must be valid UTF-8 within 4000 characters")
		}
		out.Summary = strings.TrimSpace(*in.Summary)
		if out.Summary == "" {
			return out, fmt.Errorf("summary must not be empty")
		}
	} else {
		if len(in.Fields) == 0 || len(in.Fields) > len(agentcard.EditableFields) {
			return out, fmt.Errorf("fields must contain editable Card fields")
		}
		out.Fields = make(map[string]json.RawMessage, len(in.Fields))
		for name, raw := range in.Fields {
			spec, ok := agentcard.LookupField(name)
			if !ok {
				return out, fmt.Errorf("field %q is not editable", name)
			}
			value, err := agentcard.ValidateValue(spec, raw)
			if err != nil {
				return out, err
			}
			if err := agentcard.ValidatePublicContent(spec, value); err != nil {
				return out, err
			}
			if name == "agent_name" && value.(string) == "" {
				return out, fmt.Errorf("agent_name must not be empty")
			}
			encoded, err := json.Marshal(value)
			if err != nil {
				return out, err
			}
			out.Fields[name] = encoded
		}
	}
	// Source revision is a fence, not a change in semantic content. Equivalent
	// JSON field order/whitespace must not trigger another extraction or embedding.
	body, err := json.Marshal(struct {
		Summary string                     `json:"summary,omitempty"`
		Fields  map[string]json.RawMessage `json:"fields,omitempty"`
	}{out.Summary, out.Fields})
	if err != nil {
		return out, err
	}
	sum := sha256.Sum256(body)
	out.ContentHash = hex.EncodeToString(sum[:])
	return out, nil
}

// SplitFields is used by the writer to preserve the registry's public/private
// boundary. It does not project the summary into either Card.
func (n Normalized) SplitFields() (public, private map[string]json.RawMessage) {
	public = map[string]json.RawMessage{}
	private = map[string]json.RawMessage{}
	for name, value := range n.Fields {
		spec, ok := agentcard.LookupField(name)
		if !ok {
			continue
		}
		copy := append(json.RawMessage(nil), value...)
		if spec.Public {
			public[name] = copy
		} else {
			private[name] = copy
		}
	}
	return
}
