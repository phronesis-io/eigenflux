package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

type templateData[In any] struct {
	Input In
}

// Prompt binds a template name to typed input and output structs.
type Prompt[In, Out any] struct {
	name string
}

// NewPrompt creates a typed prompt definition and registers it for startup validation.
func NewPrompt[In, Out any](name string) Prompt[In, Out] {
	p := Prompt[In, Out]{name: name}
	registeredPrompts = append(registeredPrompts, p)
	return p
}

// Execute renders the template with input, calls LLM, and unmarshals into *Out.
func (p Prompt[In, Out]) Execute(ctx context.Context, c *Client, input In) (*Out, error) {
	rendered, err := c.prompts.Render(p.name, templateData[In]{Input: input})
	if err != nil {
		return nil, fmt.Errorf("render %s prompt: %w", p.name, err)
	}
	resp, err := c.call(ctx, rendered)
	if err != nil {
		return nil, fmt.Errorf("LLM call failed: %w", err)
	}
	var result Out
	if err := json.Unmarshal([]byte(resp), &result); err != nil {
		return nil, fmt.Errorf("failed to parse %s JSON: %w, raw: %s", p.name, err, resp)
	}
	return &result, nil
}

// Name returns the template name for this prompt.
func (p Prompt[In, Out]) Name() string { return p.name }

// Validate checks that the template's JSON example keys match the Out struct's json tags.
// Templates may contain multiple JSON examples (e.g. short-circuit and full responses).
// Validation passes if the JSON object with the most keys matches the struct exactly.
func (p Prompt[In, Out]) Validate(reg *PromptRegistry) error {
	var zeroIn In
	rendered, err := reg.Render(p.name, templateData[In]{Input: zeroIn})
	if err != nil {
		return fmt.Errorf("render with zero input: %w", err)
	}

	allObjects := extractAllJSONObjects(rendered)
	if len(allObjects) == 0 {
		return fmt.Errorf("no JSON object found in rendered template")
	}

	// Find the JSON object with the most keys (the full example)
	var bestKeys []string
	for _, obj := range allObjects {
		keys, err := extractJSONObjectKeys(obj)
		if err != nil {
			continue
		}
		if len(keys) > len(bestKeys) {
			bestKeys = keys
		}
	}
	if len(bestKeys) == 0 {
		return fmt.Errorf("no parseable JSON object found in rendered template")
	}

	var zeroOut Out
	structKeys := structJSONKeys(zeroOut)

	templateKeySet := make(map[string]bool, len(bestKeys))
	for _, k := range bestKeys {
		templateKeySet[k] = true
	}
	structKeySet := make(map[string]bool, len(structKeys))
	for _, k := range structKeys {
		structKeySet[k] = true
	}

	var missing, extra []string
	for _, k := range structKeys {
		if !templateKeySet[k] {
			missing = append(missing, k)
		}
	}
	for _, k := range bestKeys {
		if !structKeySet[k] {
			extra = append(extra, k)
		}
	}

	if len(missing) > 0 || len(extra) > 0 {
		return fmt.Errorf("JSON key mismatch: missing_in_template=%v extra_in_template=%v", missing, extra)
	}
	return nil
}

// promptValidator is the interface registered prompts implement for startup validation.
type promptValidator interface {
	Name() string
	Validate(reg *PromptRegistry) error
}

var registeredPrompts []promptValidator

// ValidateAllPrompts validates all registered prompts against the loaded templates.
func ValidateAllPrompts(reg *PromptRegistry) error {
	for _, p := range registeredPrompts {
		if err := p.Validate(reg); err != nil {
			return fmt.Errorf("prompt %q validation failed: %w", p.Name(), err)
		}
	}
	return nil
}

// structJSONKeys returns the json tag names for exported fields of a struct.
// Fields with json:"-" or no json tag are excluded.
func structJSONKeys(v any) []string {
	t := reflect.TypeOf(v)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	var keys []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name != "" && name != "-" {
			keys = append(keys, name)
		}
	}
	return keys
}

// extractAllJSONObjects finds all top-level JSON objects ({...}) in a string.
func extractAllJSONObjects(text string) []string {
	var objects []string
	i := 0
	for i < len(text) {
		if text[i] != '{' {
			i++
			continue
		}
		depth := 0
		start := i
		for j := i; j < len(text); j++ {
			if text[j] == '{' {
				depth++
			} else if text[j] == '}' {
				depth--
				if depth == 0 {
					objects = append(objects, text[start:j+1])
					i = j + 1
					break
				}
			}
		}
		if depth != 0 {
			break
		}
	}
	return objects
}

// extractJSONObjectKeys parses a JSON string and returns its top-level keys.
// It strips line comments (//) and trailing ellipses (...) before parsing,
// since prompt templates often include annotated JSON examples.
func extractJSONObjectKeys(jsonStr string) ([]string, error) {
	cleaned := stripJSONComments(jsonStr)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(cleaned), &raw); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	return keys, nil
}

// stripJSONComments removes // line comments and trailing ... from JSON-like text.
func stripJSONComments(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		// Remove // comments (not inside strings — simplified: strip from first //)
		if idx := strings.Index(line, "//"); idx >= 0 {
			line = line[:idx]
		}
		// Remove trailing ... (e.g. ["a", ...])
		line = strings.ReplaceAll(line, ", ...", "")
		line = strings.ReplaceAll(line, "...", "")
		lines[i] = line
	}
	return strings.Join(lines, "\n")
}
