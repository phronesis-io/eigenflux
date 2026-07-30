package agentcard

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEditableFieldsNeverOverlapProtectedPaths(t *testing.T) {
	protected := map[string]bool{}
	for _, p := range ProtectedPaths {
		protected[p] = true
	}
	for _, f := range EditableFields {
		if protected[f.Name] {
			t.Errorf("field %q is both editable and protected", f.Name)
		}
	}
}

func TestValidateValue(t *testing.T) {
	strSpec, _ := LookupField("human_description")
	if _, err := ValidateValue(strSpec, json.RawMessage(`"a short description"`)); err != nil {
		t.Errorf("valid string rejected: %v", err)
	}
	if _, err := ValidateValue(strSpec, json.RawMessage(`["not","a","string"]`)); err == nil {
		t.Error("list accepted for a string field")
	}
	long := `"` + strings.Repeat("x", 2001) + `"`
	if _, err := ValidateValue(strSpec, json.RawMessage(long)); err == nil {
		t.Error("over-length string accepted")
	}

	listSpec, _ := LookupField("seeking")
	if _, err := ValidateValue(listSpec, json.RawMessage(`["AI infra","agent collaboration"]`)); err != nil {
		t.Errorf("valid list rejected: %v", err)
	}
	if _, err := ValidateValue(listSpec, json.RawMessage(`"not a list"`)); err == nil {
		t.Error("string accepted for a list field")
	}
	tooMany := `["` + strings.Repeat(`x","`, 25) + `x"]`
	if _, err := ValidateValue(listSpec, json.RawMessage(tooMany)); err == nil {
		t.Error("over-count list accepted")
	}

	objSpec, _ := LookupField("interrupt_threshold")
	if _, err := ValidateValue(objSpec, json.RawMessage(`{"level":"normal"}`)); err != nil {
		t.Errorf("valid object rejected: %v", err)
	}
	if _, err := ValidateValue(objSpec, json.RawMessage(`"nope"`)); err == nil {
		t.Error("string accepted for an object field")
	}

	if _, known := LookupField("influence"); known {
		t.Error("system field influence must not be editable")
	}
}
