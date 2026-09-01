package consolev2

import "testing"

func TestOnboardingLimitsExposeBackendValidationContract(t *testing.T) {
	limits := onboardingLimits()
	if got := limits.IdentityCard["agent_description"]; got.MaxChars != 1000 {
		t.Fatalf("unexpected agent_description limits: %#v", got)
	}
	if got := limits.IdentityCard["seeking"]; got.MaxChars != 300 || got.MaxItemChars != 300 || got.MaxItems != 1 {
		t.Fatalf("unexpected seeking limits: %#v", got)
	}
	if got := limits.IdentityCard["offering"]; got.MaxChars != 1000 || got.MaxItemChars != 1000 || got.MaxItems != 1 {
		t.Fatalf("unexpected offering limits: %#v", got)
	}
	if got := limits.IdentityCard["interests_negative"]; got.MaxChars != 500 || got.MaxItemChars != 500 || got.MaxItems != 1 {
		t.Fatalf("unexpected interests_negative limits: %#v", got)
	}
	if got := limits.IdentityCard["agent_status"]; got.MaxChars != 1000 || got.MaxItemChars != 1000 || got.MaxItems != 20 {
		t.Fatalf("unexpected agent_status limits: %#v", got)
	}
	if limits.NetworkGoal.MaxChars != 2000 || !limits.NetworkGoal.Required {
		t.Fatalf("unexpected network goal limits: %#v", limits.NetworkGoal)
	}
	if limits.IntentActions.MaxItems != 10 || limits.IntentActions.WatchFor.MaxChars != 1000 ||
		limits.IntentActions.TriggerWhen.MaxChars != 1000 || limits.IntentActions.ActionInstruction.MaxChars != 2000 {
		t.Fatalf("unexpected intent limits: %#v", limits.IntentActions)
	}
}
