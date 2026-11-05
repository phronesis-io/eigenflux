package integrationprofile

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestContextNormalizationSeparatesPrivateInput(t *testing.T) {
	summary := "Private project details and current research interests"
	n, err := Normalize(Input{SourceRevision: 1, Summary: &summary})
	if err != nil {
		t.Fatal(err)
	}
	public, private := n.SplitFields()
	if len(public) != 0 || len(private) != 0 || len(n.Fields) != 0 || n.Summary != summary {
		t.Fatal("summary was automatically published")
	}
	n, err = Normalize(Input{SourceRevision: 2, Fields: map[string]json.RawMessage{"current_focus": json.RawMessage(`["private research"]`), "agent_description": json.RawMessage(`"Public description"`)}})
	if err != nil {
		t.Fatal(err)
	}
	public, private = n.SplitFields()
	if len(public) != 1 || len(private) != 1 || public["current_focus"] != nil {
		t.Fatal("private Card facts escaped their boundary")
	}
}

func TestContextHashesIgnoreRevisionAndJSONOrdering(t *testing.T) {
	first, err := Normalize(Input{SourceRevision: 1, Fields: map[string]json.RawMessage{"demands": json.RawMessage(` [ "research" ] `), "agent_name": json.RawMessage(`"Agent"`)}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Normalize(Input{SourceRevision: 2, Fields: map[string]json.RawMessage{"agent_name": json.RawMessage(`"Agent"`), "demands": json.RawMessage(`["research"]`)}})
	if err != nil || first.ContentHash != second.ContentHash {
		t.Fatal("equivalent context changed extraction input", err)
	}
}

func TestContextRejectsProtectedAndSensitivePublicFields(t *testing.T) {
	empty := ""
	text := "private"
	long := strings.Repeat("x", 4001)
	for _, input := range []Input{
		{SourceRevision: 0, Summary: &text},
		{SourceRevision: 1},
		{SourceRevision: 1, Summary: &empty},
		{SourceRevision: 1, Summary: &long},
		{SourceRevision: 1, Summary: &text, Fields: map[string]json.RawMessage{}},
		{SourceRevision: 1, Fields: map[string]json.RawMessage{"runtime_name": json.RawMessage(`"official"`)}},
		{SourceRevision: 1, Fields: map[string]json.RawMessage{"agent_description": json.RawMessage(`"Contact secret@example.com"`)}},
		{SourceRevision: 1, Fields: map[string]json.RawMessage{"current_focus": json.RawMessage(`null`)}},
	} {
		if _, err := Normalize(input); err == nil {
			t.Fatalf("invalid context accepted: %+v", input)
		}
	}
}
