package llm

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

const defaultPromptsRelativePath = "static/templates/prompts"

// PromptRegistry loads and renders prompt templates from disk.
type PromptRegistry struct {
	templates map[string]*template.Template
}

// LoadPrompts reads all .tmpl files from the given directory into a registry.
func LoadPrompts(dir string) (*PromptRegistry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read prompts directory %s: %w", dir, err)
	}

	reg := &PromptRegistry{templates: make(map[string]*template.Template)}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".tmpl") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".tmpl")
		content, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read prompt template %s: %w", e.Name(), err)
		}
		tmpl, err := template.New(name).Option("missingkey=error").Parse(string(content))
		if err != nil {
			return nil, fmt.Errorf("parse prompt template %s: %w", e.Name(), err)
		}
		reg.templates[name] = tmpl
	}
	return reg, nil
}

// LoadDefaultPrompts finds the prompts directory by walking up from the working
// directory (same strategy as skilldoc) and loads all templates.
func LoadDefaultPrompts() (*PromptRegistry, error) {
	dir, err := findPromptsDir()
	if err != nil {
		return nil, err
	}
	return LoadPrompts(dir)
}

// Render executes the named template with data and returns the rendered string.
func (r *PromptRegistry) Render(name string, data any) (string, error) {
	tmpl, ok := r.templates[name]
	if !ok {
		return "", fmt.Errorf("prompt template %q not found", name)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("render prompt %q: %w", name, err)
	}
	return buf.String(), nil
}

// Names returns all loaded template names.
func (r *PromptRegistry) Names() []string {
	names := make([]string, 0, len(r.templates))
	for n := range r.templates {
		names = append(names, n)
	}
	return names
}

func findPromptsDir() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get working directory: %w", err)
	}
	for dir := wd; ; dir = filepath.Dir(dir) {
		candidate := filepath.Join(dir, defaultPromptsRelativePath)
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("prompts directory not found: %s", defaultPromptsRelativePath)
		}
	}
}
