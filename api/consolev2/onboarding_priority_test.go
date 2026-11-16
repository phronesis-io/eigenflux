package consolev2

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestOnboardingDraftIntentPriority(t *testing.T) {
	for _, tt := range []struct {
		name    string
		field   string
		want    int16
		wantErr bool
	}{
		{name: "omitted"},
		{name: "null", field: `"priority":null`},
		{name: "integer", field: `"priority":10`, want: 10},
		{name: "integer string", field: `"priority":"10"`, want: 10},
		{name: "negative integer string", field: `"priority":"-32768"`, want: -32768},
		{name: "non-numeric label", field: `"priority":"high"`, wantErr: true},
		{name: "fraction", field: `"priority":"1.5"`, wantErr: true},
		{name: "overflow", field: `"priority":"32768"`, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw := `{"intent_actions":[{` + tt.field + `}]}`
			var payload draftPayload
			err := json.Unmarshal([]byte(raw), &payload)
			if (err != nil) != tt.wantErr {
				t.Fatalf("decode error = %v, want error %t", err, tt.wantErr)
			}
			if !tt.wantErr && int16(payload.IntentActions[0].Priority) != tt.want {
				t.Fatalf("priority = %d, want %d", payload.IntentActions[0].Priority, tt.want)
			}
		})
	}
}

func TestDecodeOnboardingDraftStepChecksOnlyCurrentSection(t *testing.T) {
	draft := []byte(`{
		"identity_card":{"agent_name":"Agent"},
		"network_goal":"Find infrastructure updates",
		"intent_actions":[{"priority":"high"}],
		"security_boundary":{"auto_reply_pm":"false"}
	}`)
	for _, step := range []int16{2, 3} {
		payload, err := decodeOnboardingDraftStep(draft, step)
		if err != nil {
			t.Fatalf("step %d was blocked by another section: %v", step, err)
		}
		if err := validateDraftStep(payload, step); err != nil {
			t.Fatalf("step %d validation failed: %v", step, err)
		}
	}
	for _, step := range []int16{4, 5} {
		if _, err := decodeOnboardingDraftStep(draft, step); err == nil {
			t.Fatalf("step %d accepted an invalid field in its own section", step)
		}
	}
}

func TestOnboardingDraftDecodeLogFields(t *testing.T) {
	for _, tt := range []struct {
		name, priority string
		want           []interface{}
	}{
		{"known label", `"HIGH"`, []interface{}{"reason", "invalid_integer_string", "field", "intent_actions.priority", "expected_type", "int16", "received_type", "string", "received_label", "high"}},
		{"arbitrary text", `"private draft text"`, []interface{}{"reason", "invalid_integer_string", "field", "intent_actions.priority", "expected_type", "int16", "received_type", "string"}},
		{"string out of range", `"32768"`, []interface{}{"reason", "integer_out_of_range", "field", "intent_actions.priority", "expected_type", "int16", "received_type", "string"}},
		{"numeric literal omitted", `123456789`, []interface{}{"reason", "type_mismatch", "field", "priority", "expected_type", "int16", "received_type", "number"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			draft := []byte(`{"intent_actions":[{"priority":` + tt.priority + `}]}`)
			_, err := decodeOnboardingDraftStep(draft, 4)
			if err == nil {
				t.Fatal("expected priority decoding to fail")
			}
			if got := onboardingDraftDecodeLogFields(err); !slices.Equal(got, tt.want) {
				t.Fatalf("log fields = %v, want %v", got, tt.want)
			}
		})
	}
}
