package consolev2

import (
	"encoding/json"
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
