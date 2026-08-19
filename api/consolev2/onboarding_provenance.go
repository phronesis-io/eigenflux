package consolev2

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
)

const (
	provenanceAgent  = "agent_prefill"
	provenanceHuman  = "human_edit"
	provenanceSystem = "system_derived"
)

var onboardingDraftFieldPaths = []string{
	"identity_card.agent_name",
	"identity_card.bio",
	"identity_card.agent_description",
	"identity_card.human_description",
	"identity_card.working_languages",
	"identity_card.seeking",
	"identity_card.offering",
	"identity_card.geo",
	"identity_card.timezone",
	"identity_card.agent_status",
	"identity_card.human_status",
	"identity_card.interests_negative",
	"security_boundary.recurring_publish",
	"security_boundary.auto_reply_pm",
	"security_boundary.auto_comment",
	"security_boundary.show_add_friend",
	"network_goal",
	"intent_actions",
}

func decodeJSONObject(raw json.RawMessage) (map[string]interface{}, error) {
	value := map[string]interface{}{}
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return value, nil
}

func decodeProvenance(raw json.RawMessage) map[string]string {
	values := map[string]string{}
	if len(raw) == 0 {
		return values
	}
	_ = json.Unmarshal(raw, &values)
	for path, source := range values {
		if !validProvenance(source) {
			delete(values, path)
		}
	}
	return values
}

func validProvenance(source string) bool {
	switch source {
	case provenanceAgent, provenanceHuman, provenanceSystem:
		return true
	default:
		return false
	}
}

func draftPathValue(root map[string]interface{}, path string) (interface{}, bool) {
	parts := strings.Split(path, ".")
	var current interface{} = root
	for _, part := range parts {
		object, ok := current.(map[string]interface{})
		if !ok {
			return nil, false
		}
		current, ok = object[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func setDraftPathValue(root map[string]interface{}, path string, value interface{}, exists bool) {
	parts := strings.Split(path, ".")
	current := root
	for _, part := range parts[:len(parts)-1] {
		next, ok := current[part].(map[string]interface{})
		if !ok {
			next = map[string]interface{}{}
			current[part] = next
		}
		current = next
	}
	leaf := parts[len(parts)-1]
	if exists {
		current[leaf] = value
	} else {
		delete(current, leaf)
	}
}

func meaningfulDraftValue(value interface{}, exists bool) bool {
	if !exists || value == nil {
		return false
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed) != ""
	case []interface{}:
		return len(typed) > 0
	case map[string]interface{}:
		return len(typed) > 0
	default:
		// Booleans are explicit settings even when false; numeric zero may also
		// be an intentional value in future additive fields.
		return true
	}
}

// deriveInitialProvenance records only fields that the Agent actually
// supplied. Empty placeholders stay unlabelled so the Web cannot claim they
// were prefilled when no value exists.
func deriveInitialProvenance(draft map[string]interface{}, source string) map[string]string {
	result := map[string]string{}
	if !validProvenance(source) {
		return result
	}
	for _, path := range onboardingDraftFieldPaths {
		value, exists := draftPathValue(draft, path)
		if meaningfulDraftValue(value, exists) {
			result[path] = source
		}
	}
	return result
}

// mergeOnboardingDraft enforces field ownership at the server boundary. A
// human edit wins permanently for that draft field; later Agent prefills are
// skipped and reported instead of silently overwriting the user's choice.
func mergeOnboardingDraft(previous, incoming map[string]interface{}, previousProvenance map[string]string, actor string) (map[string]interface{}, map[string]string, []string) {
	merged := incoming
	provenance := make(map[string]string, len(previousProvenance))
	for path, source := range previousProvenance {
		if validProvenance(source) {
			provenance[path] = source
		}
	}
	blocked := make([]string, 0)
	for _, path := range onboardingDraftFieldPaths {
		oldValue, oldExists := draftPathValue(previous, path)
		newValue, newExists := draftPathValue(incoming, path)
		changed := oldExists != newExists || !reflect.DeepEqual(oldValue, newValue)

		if actor == provenanceAgent && provenance[path] == provenanceHuman {
			setDraftPathValue(merged, path, oldValue, oldExists)
			if newExists {
				blocked = append(blocked, path)
			}
			continue
		}
		if !changed {
			continue
		}
		if actor == provenanceHuman {
			// Keep human ownership even when the value was cleared; otherwise a
			// later Agent prefill could resurrect data the user deliberately removed.
			provenance[path] = provenanceHuman
			continue
		}
		if meaningfulDraftValue(newValue, newExists) {
			provenance[path] = provenanceAgent
		} else {
			delete(provenance, path)
		}
	}
	sort.Strings(blocked)
	return merged, provenance, blocked
}

func canonicalSource(provenance map[string]string, path string) string {
	if source := provenance[path]; validProvenance(source) {
		return source
	}
	return provenanceHuman
}
