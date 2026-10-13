package need

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestExecutionInputPreservesBothSourceProtocols(t *testing.T) {
	for _, raw := range []string{
		`{"schema_version":"need_input.v1","intent_id":"42","intent_version":1,"need_type":"agent","target":{"desc":"  design  ","candidate_needs":["web","landing page"]},"preferences":"Chinese preferred","constraints":{"lang":["English"]}}`,
		`{"schema_version":"need_input.v2","intent_id":"42","intent_version":1,"need_type":"agent","target":{"goal":"  design  ","context":"landing page"},"requirements":[{"text":"No data uploads","source_quote":"no uploads"}],"preferences":[{"text":"Chinese preferred"}],"constraints":{"lang":["EN"],"provider_region":["us"]}}`,
	} {
		s := Snapshot{InputID: 1, IntentID: 42, IntentVersion: 1, Input: json.RawMessage(raw)}
		in, err := s.ExecutionInput()
		if err != nil || in.Target.Goal != "  design  " || len(in.Preferences) != 1 || string(s.Input) != raw {
			t.Fatal(in, err)
		}
		c, ok := ExecutionConstraints(in.Constraints)
		if in.Constraints.Lang[0] == "English" {
			if ok || !reflect.DeepEqual(c.Lang, []string{"English"}) {
				t.Fatal("legacy restriction interpreted", c)
			}
		} else {
			if !ok || !reflect.DeepEqual(c.Lang, []string{"en"}) || !reflect.DeepEqual(c.ProviderRegion, []string{"US"}) || in.Constraints.Lang[0] != "EN" || len(in.Requirements) != 1 {
				t.Fatal(c, in)
			}
		}
	}
}
func TestUnknownAlternativeDoesNotNarrowExecutionRestriction(t *testing.T) {
	c := Constraints{Lang: []string{"en", "English"}}
	got, ok := ExecutionConstraints(c)
	if ok || !reflect.DeepEqual(got, c) {
		t.Fatal(got, ok)
	}
}
