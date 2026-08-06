package agentcard

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// SchemaVersion is the Card JSON schema revision served to clients.
const SchemaVersion int32 = 4

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
}

// ProtectedPaths are Card paths no client may write, whatever the request
// claims. Served in refresh-context so agents don't have to guess.
var ProtectedPaths = []string{
	"agent_id",
	"joined_at",
	"runtime",
	"runtime_mode",
	"runtime_name",
	"runtime_version",
	"is_official",
	"verification",
	"last_active_at",
	"influence",
	"relations",
	"interests_positive",
	"delivery_preference",
	"interrupt_threshold",
	"card_version",
	"generated_at",
	"updated_at",
}

var publicSensitivePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}`),
	regexp.MustCompile(`(?i)\b(?:https?://|www\.)\S+`),
	regexp.MustCompile(`(?i)\b(?:sk-[a-z0-9_\-]{16,}|ghp_[a-z0-9]{20,}|github_pat_[a-z0-9_]{20,}|xox[baprs]-[a-z0-9-]{16,}|bearer\s+[a-z0-9._\-]{16,}|api[_-]?key\s*[:=])`),
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	regexp.MustCompile(`(?i)-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----`),
	regexp.MustCompile(`(?i)\b(?:localhost|127\.0\.0\.1|10\.(?:\d{1,3}\.){2}\d{1,3}|192\.168\.(?:\d{1,3}\.)\d{1,3}|172\.(?:1[6-9]|2\d|3[01])\.(?:\d{1,3}\.)\d{1,3}|[a-z0-9.-]+\.internal)\b`),
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
	if string(bytes.TrimSpace(raw)) == "null" {
		return nil, fmt.Errorf("field %q must not be null", spec.Name)
	}
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

// ValidatePublicContent rejects high-confidence secret/contact/link patterns
// before editable values can enter the network-visible card. Natural-language
// privacy (real names, employers, clients) still requires the refresh prompt's
// generalization rule; those categories cannot be detected reliably here.
func ValidatePublicContent(spec FieldSpec, value interface{}) error {
	if !spec.Public {
		return nil
	}
	var values []string
	switch v := value.(type) {
	case string:
		values = []string{v}
	case []string:
		values = v
	default:
		return nil
	}
	for _, value := range values {
		for _, pattern := range publicSensitivePatterns {
			if pattern.MatchString(strings.TrimSpace(value)) {
				return fmt.Errorf("field %q contains contact, credential, or URL data that cannot be published", spec.Name)
			}
		}
	}
	return nil
}
