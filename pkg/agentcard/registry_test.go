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

func TestValidatePublicContentBlocksHighConfidenceLeaks(t *testing.T) {
	public, _ := LookupField("human_description")
	for _, value := range []string{
		"contact me at person@example.com",
		"internal notes at https://corp.internal/runbook",
		"api_key=super-secret-value",
		"service is on localhost",
		"token ghp_123456789012345678901234567890123456",
		"aws key AKIA1234567890ABCDEF",
		"private host 192.168.1.12",
		"private host 172.20.1.12",
		"-----BEGIN PRIVATE KEY-----",
	} {
		if err := ValidatePublicContent(public, value); err == nil {
			t.Errorf("public sensitive value accepted: %q", value)
		}
	}
	if err := ValidatePublicContent(public, "Works on generalized fintech infrastructure"); err != nil {
		t.Errorf("safe generalized public value rejected: %v", err)
	}
	private, _ := LookupField("current_focus")
	if err := ValidatePublicContent(private, []string{"debugging localhost"}); err != nil {
		t.Errorf("private field unexpectedly subjected to public-content guard: %v", err)
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

	for _, spec := range []FieldSpec{strSpec, listSpec} {
		if _, err := ValidateValue(spec, json.RawMessage(`null`)); err == nil {
			t.Errorf("%s field accepted null", spec.Kind)
		}
	}
	if _, known := LookupField("interrupt_threshold"); known {
		t.Error("system-owned interrupt_threshold must not be editable")
	}

	if _, known := LookupField("influence"); known {
		t.Error("system field influence must not be editable")
	}
}
