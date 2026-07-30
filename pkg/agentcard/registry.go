package agentcard

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

// SchemaVersion is the Card JSON schema revision served to clients.
const SchemaVersion int32 = 1

// FieldStorage says which fact table owns an editable field. The Card itself
// is never a fact source.
type FieldStorage int

const (
	// StorageAgents: column on the agents table (agent_name, bio).
	StorageAgents FieldStorage = iota
	// StorageProfileData: key inside agent_profiles.profile_data JSONB.
	StorageProfileData
)

// FieldSpec declares one Human/Agent-editable Card field: where it lives,
// which card it projects into, and its validation bounds. Fields not listed
// here are system-owned and rejected on write.
type FieldSpec struct {
	Name   string
	Public bool // projects into public_card (otherwise private_card)
	// Storage tells the writer where to persist the value.
	Storage FieldStorage
	// Kind: "string" | "string_list" | "object".
	Kind string
	// MaxLen bounds a string value, or each item of a string_list (runes).
	MaxLen int
	// MaxItems bounds a string_list.
	MaxItems int
}

// EditableFields is the single authority on what PUT /agents/me/profile/fields
// accepts. agent_name / agent_description keep their legacy homes on agents
// (bio doubles as agent_description during the compat window); everything
// else lives in agent_profiles.profile_data.
var EditableFields = []FieldSpec{
	{Name: "agent_name", Public: true, Storage: StorageAgents, Kind: "string", MaxLen: 100},
	{Name: "agent_description", Public: true, Storage: StorageAgents, Kind: "string", MaxLen: 4000},
	{Name: "human_description", Public: true, Storage: StorageProfileData, Kind: "string", MaxLen: 2000},
	{Name: "working_languages", Public: true, Storage: StorageProfileData, Kind: "string_list", MaxLen: 32, MaxItems: 10},
	{Name: "seeking", Public: true, Storage: StorageProfileData, Kind: "string_list", MaxLen: 100, MaxItems: 20},
	{Name: "offering", Public: true, Storage: StorageProfileData, Kind: "string_list", MaxLen: 100, MaxItems: 20},

	{Name: "geo", Public: false, Storage: StorageProfileData, Kind: "string", MaxLen: 100},
	{Name: "timezone", Public: false, Storage: StorageProfileData, Kind: "string", MaxLen: 64},
	{Name: "current_focus", Public: false, Storage: StorageProfileData, Kind: "string_list", MaxLen: 200, MaxItems: 20},
	{Name: "demands", Public: false, Storage: StorageProfileData, Kind: "string_list", MaxLen: 200, MaxItems: 20},
	{Name: "agent_status", Public: false, Storage: StorageProfileData, Kind: "string_list", MaxLen: 200, MaxItems: 20},
	{Name: "human_status", Public: false, Storage: StorageProfileData, Kind: "string_list", MaxLen: 200, MaxItems: 20},
	{Name: "interests_negative", Public: false, Storage: StorageProfileData, Kind: "string_list", MaxLen: 100, MaxItems: 30},
	{Name: "interrupt_threshold", Public: false, Storage: StorageProfileData, Kind: "object", MaxLen: 2048},
}

// ProtectedPaths are Card paths no client may write, whatever the request
// claims. Served in refresh-context so agents don't have to guess.
var ProtectedPaths = []string{
	"agent_id",
	"joined_at",
	"runtime",
	"is_official",
	"verification",
	"last_active_at",
	"influence",
	"relations",
	"interests_positive",
	"delivery_preference",
	"card_version",
	"generated_at",
}

var editableByName = func() map[string]FieldSpec {
	m := make(map[string]FieldSpec, len(EditableFields))
	for _, f := range EditableFields {
		m[f.Name] = f
	}
	return m
}()

// LookupField returns the spec for an editable field name.
func LookupField(name string) (FieldSpec, bool) {
	f, ok := editableByName[name]
	return f, ok
}

// ValidateValue checks raw JSON against the field's spec and returns the
// normalized value (string / []string / map). Errors are user-facing.
func ValidateValue(spec FieldSpec, raw json.RawMessage) (interface{}, error) {
	switch spec.Kind {
	case "string":
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("field %q must be a string", spec.Name)
		}
		if utf8.RuneCountInString(s) > spec.MaxLen {
			return nil, fmt.Errorf("field %q exceeds %d characters", spec.Name, spec.MaxLen)
		}
		return s, nil
	case "string_list":
		var list []string
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, fmt.Errorf("field %q must be a list of strings", spec.Name)
		}
		if len(list) > spec.MaxItems {
			return nil, fmt.Errorf("field %q exceeds %d items", spec.Name, spec.MaxItems)
		}
		for _, item := range list {
			if utf8.RuneCountInString(item) > spec.MaxLen {
				return nil, fmt.Errorf("field %q has an item exceeding %d characters", spec.Name, spec.MaxLen)
			}
		}
		return list, nil
	case "object":
		if len(raw) > spec.MaxLen {
			return nil, fmt.Errorf("field %q exceeds %d bytes", spec.Name, spec.MaxLen)
		}
		var obj map[string]interface{}
		if err := json.Unmarshal(raw, &obj); err != nil {
			return nil, fmt.Errorf("field %q must be a JSON object", spec.Name)
		}
		return obj, nil
	default:
		return nil, fmt.Errorf("field %q has unsupported kind", spec.Name)
	}
}
