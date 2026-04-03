package llm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStructJSONKeys(t *testing.T) {
	type Example struct {
		Name    string   `json:"name"`
		Score   float64  `json:"score"`
		Tags    []string `json:"tags"`
		private string
	}

	keys := structJSONKeys(Example{})
	expected := map[string]bool{"name": true, "score": true, "tags": true}
	if len(keys) != len(expected) {
		t.Fatalf("expected %d keys, got %d: %v", len(expected), len(keys), keys)
	}
	for _, k := range keys {
		if !expected[k] {
			t.Errorf("unexpected key: %q", k)
		}
	}
}

func TestStructJSONKeys_OmitemptyAndDash(t *testing.T) {
	type Example struct {
		Visible string `json:"visible,omitempty"`
		Hidden  string `json:"-"`
		NoTag   string
	}

	keys := structJSONKeys(Example{})
	if len(keys) != 1 {
		t.Fatalf("expected 1 key, got %d: %v", len(keys), keys)
	}
	if keys[0] != "visible" {
		t.Errorf("expected 'visible', got %q", keys[0])
	}
}

func TestExtractJSONObjectKeys(t *testing.T) {
	keys, err := extractJSONObjectKeys(`{"name": "test", "score": 0.5, "tags": ["a"]}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := map[string]bool{"name": true, "score": true, "tags": true}
	if len(keys) != len(expected) {
		t.Fatalf("expected %d keys, got %d: %v", len(expected), len(keys), keys)
	}
	for _, k := range keys {
		if !expected[k] {
			t.Errorf("unexpected key: %q", k)
		}
	}
}

func TestExtractJSONObjectKeys_InvalidJSON(t *testing.T) {
	_, err := extractJSONObjectKeys("not json")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestValidatePrompt_Matching(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "test.tmpl"), []byte(`Analyze this: {{ .Input.Text }}
Return: {"name": "example", "score": 0.5}`), 0644)

	reg, err := LoadPrompts(dir)
	if err != nil {
		t.Fatal(err)
	}

	type In struct{ Text string }
	type Out struct {
		Name  string  `json:"name"`
		Score float64 `json:"score"`
	}
	p := Prompt[In, Out]{name: "test"}

	if err := p.Validate(reg); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidatePrompt_MissingInTemplate(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "test.tmpl"), []byte(`Analyze: {{ .Input.Text }}
Return: {"name": "example"}`), 0644)

	reg, err := LoadPrompts(dir)
	if err != nil {
		t.Fatal(err)
	}

	type In struct{ Text string }
	type Out struct {
		Name  string  `json:"name"`
		Score float64 `json:"score"`
	}
	p := Prompt[In, Out]{name: "test"}

	err = p.Validate(reg)
	if err == nil {
		t.Fatal("expected error for missing key in template")
	}
	if !strings.Contains(err.Error(), "score") {
		t.Errorf("error should mention 'score': %v", err)
	}
}

func TestValidatePrompt_ExtraInTemplate(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "test.tmpl"), []byte(`Analyze: {{ .Input.Text }}
Return: {"name": "example", "score": 0.5, "bonus": true}`), 0644)

	reg, err := LoadPrompts(dir)
	if err != nil {
		t.Fatal(err)
	}

	type In struct{ Text string }
	type Out struct {
		Name  string  `json:"name"`
		Score float64 `json:"score"`
	}
	p := Prompt[In, Out]{name: "test"}

	err = p.Validate(reg)
	if err == nil {
		t.Fatal("expected error for extra key in template")
	}
	if !strings.Contains(err.Error(), "bonus") {
		t.Errorf("error should mention 'bonus': %v", err)
	}
}

func TestValidateAllPrompts_RealTemplates(t *testing.T) {
	reg := testPromptRegistry(t)
	if err := ValidateAllPrompts(reg); err != nil {
		t.Fatalf("ValidateAllPrompts failed: %v", err)
	}
}
