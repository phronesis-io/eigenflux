package consolev2

import "eigenflux_server/pkg/agentcard"

type onboardingFieldLimit struct {
	MaxChars     int  `json:"max_chars,omitempty"`
	MaxItems     int  `json:"max_items,omitempty"`
	MaxItemChars int  `json:"max_item_chars,omitempty"`
	Required     bool `json:"required,omitempty"`
}

type onboardingLimitsPayload struct {
	IdentityCard  map[string]onboardingFieldLimit `json:"identity_card"`
	NetworkGoal   onboardingFieldLimit            `json:"network_goal"`
	IntentActions struct {
		MaxItems          int                  `json:"max_items"`
		WatchFor          onboardingFieldLimit `json:"watch_for"`
		TriggerWhen       onboardingFieldLimit `json:"trigger_when"`
		ActionInstruction onboardingFieldLimit `json:"action_instruction"`
	} `json:"intent_actions"`
}

func onboardingLimits() onboardingLimitsPayload {
	limits := onboardingLimitsPayload{
		IdentityCard: make(map[string]onboardingFieldLimit),
		NetworkGoal: onboardingFieldLimit{
			MaxChars: onboardingNetworkGoalMaxChars,
			Required: true,
		},
	}
	for _, spec := range agentcard.EditableFields {
		if spec.Name == "current_focus" || spec.Name == "demands" {
			continue
		}
		fieldLimit := onboardingFieldLimit{Required: spec.Name == "agent_name"}
		if productLimit, ok := agentcard.ConsoleV2FieldLimits[spec.Name]; ok {
			fieldLimit.MaxChars = productLimit
		} else if spec.Kind == "string" {
			fieldLimit.MaxChars = spec.MaxLen
		}
		if spec.Kind == "string_list" {
			fieldLimit.MaxItems = spec.MaxItems
			fieldLimit.MaxItemChars = spec.MaxLen
		}
		limits.IdentityCard[spec.Name] = fieldLimit
	}
	limits.IntentActions.MaxItems = onboardingIntentMaxItems
	limits.IntentActions.WatchFor = onboardingFieldLimit{MaxChars: onboardingIntentWatchForMaxChars, Required: true}
	limits.IntentActions.TriggerWhen = onboardingFieldLimit{MaxChars: onboardingIntentTriggerWhenMaxChars}
	limits.IntentActions.ActionInstruction = onboardingFieldLimit{MaxChars: onboardingIntentInstructionMaxChars}
	return limits
}
