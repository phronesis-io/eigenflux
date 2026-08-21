package consolev2

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

const (
	provenanceAgent  = "agent_prefill"
	provenanceHuman  = "human_edit"
	provenanceSystem = "system_derived"

	fieldSourceAgentInferred    = "agent_inferred"
	fieldSourceAgentUserContext = "agent_user_context"
	fieldSourceHumanInput       = "human_input"
	fieldSourceSystemGenerated  = "system_generated"

	fieldActorAgent  = "agent"
	fieldActorHuman  = "human"
	fieldActorSystem = "system"
)

type fieldProvenance struct {
	OriginSource   string `json:"origin_source"`
	ValueSource    string `json:"value_source"`
	LastActorType  string `json:"last_actor_type"`
	HumanConfirmed bool   `json:"human_confirmed"`
	UpdatedAt      int64  `json:"updated_at"`
}

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

func decodeProvenance(raw json.RawMessage) map[string]fieldProvenance {
	values := map[string]fieldProvenance{}
	if len(raw) == 0 {
		return values
	}
	var encoded map[string]json.RawMessage
	if json.Unmarshal(raw, &encoded) != nil {
		return values
	}
	for path, entryRaw := range encoded {
		var entry fieldProvenance
		if json.Unmarshal(entryRaw, &entry) == nil && validFieldSource(entry.OriginSource) &&
			validFieldSource(entry.ValueSource) && validFieldActor(entry.LastActorType) {
			values[path] = entry
			continue
		}
		// Accept the pre-release string representation while test environments
		// roll forward. New responses always use the structured representation.
		var legacyActor string
		if json.Unmarshal(entryRaw, &legacyActor) == nil {
			if converted, ok := provenanceForActor(legacyActor, 0); ok {
				values[path] = converted
			}
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

func validFieldSource(source string) bool {
	switch source {
	case fieldSourceAgentInferred, fieldSourceAgentUserContext, fieldSourceHumanInput, fieldSourceSystemGenerated:
		return true
	default:
		return false
	}
}

func validAgentFieldSource(source string) bool {
	return source == fieldSourceAgentInferred || source == fieldSourceAgentUserContext || source == fieldSourceSystemGenerated
}

func validFieldActor(actor string) bool {
	return actor == fieldActorAgent || actor == fieldActorHuman || actor == fieldActorSystem
}

func provenanceForActor(actor string, now int64) (fieldProvenance, bool) {
	switch actor {
	case provenanceAgent:
		return fieldProvenance{OriginSource: fieldSourceAgentInferred, ValueSource: fieldSourceAgentInferred, LastActorType: fieldActorAgent, UpdatedAt: now}, true
	case provenanceHuman:
		return fieldProvenance{OriginSource: fieldSourceHumanInput, ValueSource: fieldSourceHumanInput, LastActorType: fieldActorHuman, HumanConfirmed: true, UpdatedAt: now}, true
	case provenanceSystem:
		return fieldProvenance{OriginSource: fieldSourceSystemGenerated, ValueSource: fieldSourceSystemGenerated, LastActorType: fieldActorSystem, UpdatedAt: now}, true
	default:
		return fieldProvenance{}, false
	}
}

func knownDraftFieldPath(path string) bool {
	for _, candidate := range onboardingDraftFieldPaths {
		if path == candidate {
			return true
		}
	}
	return false
}

func validateRequestedAgentProvenance(values map[string]string) error {
	for path, source := range values {
		if !knownDraftFieldPath(path) || !validAgentFieldSource(source) {
			return fmt.Errorf("invalid Agent field provenance for %s", path)
		}
	}
	return nil
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
func deriveInitialProvenance(draft map[string]interface{}, actor string, requested map[string]string, now int64) map[string]fieldProvenance {
	result := map[string]fieldProvenance{}
	base, ok := provenanceForActor(actor, now)
	if !ok {
		return result
	}
	for _, path := range onboardingDraftFieldPaths {
		value, exists := draftPathValue(draft, path)
		if meaningfulDraftValue(value, exists) {
			entry := base
			if actor == provenanceAgent {
				if source := requested[path]; validAgentFieldSource(source) {
					entry.OriginSource = source
					entry.ValueSource = source
					if source == fieldSourceSystemGenerated {
						entry.LastActorType = fieldActorSystem
					}
				}
			}
			result[path] = entry
		}
	}
	return result
}

// mergeOnboardingDraft enforces field ownership at the server boundary. A
// human edit wins permanently for that draft field; later Agent prefills are
// skipped and reported instead of silently overwriting the user's choice.
func mergeOnboardingDraft(previous, incoming map[string]interface{}, previousProvenance map[string]fieldProvenance, actor string, requested map[string]string, now int64) (map[string]interface{}, map[string]fieldProvenance, []string) {
	merged := incoming
	provenance := make(map[string]fieldProvenance, len(previousProvenance))
	for path, entry := range previousProvenance {
		if validFieldSource(entry.OriginSource) && validFieldSource(entry.ValueSource) && validFieldActor(entry.LastActorType) {
			provenance[path] = entry
		}
	}
	blocked := make([]string, 0)
	for _, path := range onboardingDraftFieldPaths {
		oldValue, oldExists := draftPathValue(previous, path)
		newValue, newExists := draftPathValue(incoming, path)
		changed := oldExists != newExists || !reflect.DeepEqual(oldValue, newValue)

		entry, hasEntry := provenance[path]
		if actor == provenanceAgent && hasEntry && entry.HumanConfirmed {
			setDraftPathValue(merged, path, oldValue, oldExists)
			if newExists {
				blocked = append(blocked, path)
			}
			continue
		}
		if !changed {
			if actor == provenanceAgent && meaningfulDraftValue(newValue, newExists) {
				if source := requested[path]; validAgentFieldSource(source) && !entry.HumanConfirmed {
					if !hasEntry {
						entry.OriginSource = source
					}
					entry.ValueSource = source
					entry.LastActorType = fieldActorAgent
					if source == fieldSourceSystemGenerated {
						entry.LastActorType = fieldActorSystem
					}
					entry.UpdatedAt = now
					provenance[path] = entry
				}
			}
			continue
		}
		if actor == provenanceHuman {
			if !hasEntry {
				entry.OriginSource = fieldSourceHumanInput
			}
			entry.ValueSource = fieldSourceHumanInput
			entry.LastActorType = fieldActorHuman
			entry.HumanConfirmed = true
			entry.UpdatedAt = now
			provenance[path] = entry
			continue
		}
		if meaningfulDraftValue(newValue, newExists) {
			base, _ := provenanceForActor(actor, now)
			if actor == provenanceAgent {
				source := requested[path]
				if !validAgentFieldSource(source) {
					source = fieldSourceAgentInferred
				}
				base.OriginSource = source
				base.ValueSource = source
				if source == fieldSourceSystemGenerated {
					base.LastActorType = fieldActorSystem
				}
			}
			if hasEntry {
				base.OriginSource = entry.OriginSource
			}
			provenance[path] = base
		} else {
			delete(provenance, path)
		}
	}
	sort.Strings(blocked)
	return merged, provenance, blocked
}

func confirmStepProvenance(draft map[string]interface{}, provenance map[string]fieldProvenance, step int16, now int64) map[string]fieldProvenance {
	paths := onboardingDraftFieldPaths
	switch step {
	case 2:
		paths = onboardingDraftFieldPaths[:12]
	case 3:
		paths = onboardingDraftFieldPaths[16:17]
	case 4:
		paths = onboardingDraftFieldPaths[17:18]
	case 5:
		paths = onboardingDraftFieldPaths[12:16]
	}
	for _, path := range paths {
		value, exists := draftPathValue(draft, path)
		entry, ok := provenance[path]
		if !ok && !meaningfulDraftValue(value, exists) {
			continue
		}
		if !ok {
			entry = fieldProvenance{OriginSource: fieldSourceHumanInput, ValueSource: fieldSourceHumanInput}
		}
		entry.LastActorType = fieldActorHuman
		entry.HumanConfirmed = true
		entry.UpdatedAt = now
		provenance[path] = entry
	}
	return provenance
}

func canonicalSource(provenance map[string]fieldProvenance, path string) string {
	if entry, ok := provenance[path]; ok {
		switch entry.ValueSource {
		case fieldSourceAgentInferred, fieldSourceAgentUserContext:
			return provenanceAgent
		case fieldSourceSystemGenerated:
			return provenanceSystem
		case fieldSourceHumanInput:
			return provenanceHuman
		}
	}
	return provenanceHuman
}
