package llm

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPrompts(t *testing.T) {
	dir := t.TempDir()

	// Write a test template
	err := os.WriteFile(filepath.Join(dir, "greet.tmpl"), []byte("Hello, {{ .Name }}!"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	reg, err := LoadPrompts(dir)
	if err != nil {
		t.Fatalf("LoadPrompts: %v", err)
	}

	if len(reg.Names()) != 1 {
		t.Fatalf("expected 1 template, got %d", len(reg.Names()))
	}

	rendered, err := reg.Render("greet", map[string]string{"Name": "World"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if rendered != "Hello, World!" {
		t.Errorf("expected 'Hello, World!', got %q", rendered)
	}
}

func TestRender_MissingTemplate(t *testing.T) {
	reg, err := LoadPrompts(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	_, err = reg.Render("nonexistent", nil)
	if err == nil {
		t.Fatal("expected error for missing template")
	}
}

func TestRender_MissingKey(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "test.tmpl"), []byte("{{ .Required }}"), 0644)

	reg, err := LoadPrompts(dir)
	if err != nil {
		t.Fatal(err)
	}

	_, err = reg.Render("test", map[string]string{})
	if err == nil {
		t.Fatal("expected error for missing key")
	}
}

func TestLoadDefaultPrompts(t *testing.T) {
	reg := testPromptRegistry(t)

	names := reg.Names()
	if len(names) < 2 {
		t.Fatalf("expected at least 2 templates, got %d: %v", len(names), names)
	}

	// Verify process_item renders with .Input. prefix
	rendered, err := reg.Render("process_item", struct{ Input ProcessItemInput }{
		Input: ProcessItemInput{Content: "test content", Notes: "test notes"},
	})
	if err != nil {
		t.Fatalf("Render process_item: %v", err)
	}
	if len(rendered) == 0 {
		t.Fatal("process_item rendered empty")
	}

	// Verify extract_keywords renders with .Input. prefix
	rendered, err = reg.Render("extract_keywords", struct{ Input ExtractKeywordsInput }{
		Input: ExtractKeywordsInput{Bio: "I love Go programming"},
	})
	if err != nil {
		t.Fatalf("Render extract_keywords: %v", err)
	}
	if len(rendered) == 0 {
		t.Fatal("extract_keywords rendered empty")
	}
}
