package consumer

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const enrichmentVersion = 1

// EnrichLLM is the minimal LLM surface EnrichService depends on.
// pipeline/llm.Client gets wrapped to satisfy this in production (see T5).
type EnrichLLM interface {
	Complete(ctx context.Context, prompt string) (string, error)
}

// EnrichInput carries the structured slice of the service used to build the prompt.
type EnrichInput struct {
	Title          string
	CapabilityDesc string
	CallSpecText   string
	CallSpecSchema string // optional raw JSON; "" when not set
}

// EnrichOutput is the parsed LLM response.
type EnrichOutput struct {
	CapabilityTags    []string        `json:"capability_tags"`
	UseCases          string          `json:"use_cases"`
	CanonicalInputs   json.RawMessage `json:"canonical_inputs"`
	CanonicalOutputs  json.RawMessage `json:"canonical_outputs"`
	EnrichmentVersion int             `json:"-"`
}

// EnrichService asks the LLM to produce capability_tags + use_cases + canonical_io
// from the service self-description. The prompt is framed from the buyer's
// perspective ("Use when...").
func EnrichService(ctx context.Context, llm EnrichLLM, in EnrichInput) (*EnrichOutput, error) {
	prompt := buildEnrichmentPrompt(in)
	resp, err := llm.Complete(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("enrich llm: %w", err)
	}
	cleaned := strings.TrimSpace(resp)
	cleaned = stripCodeFence(cleaned)
	var out EnrichOutput
	if err := json.Unmarshal([]byte(cleaned), &out); err != nil {
		return nil, fmt.Errorf("enrich parse: %w (resp=%q)", err, truncateForErr(cleaned))
	}
	if out.UseCases == "" {
		return nil, fmt.Errorf("enrich: empty use_cases")
	}
	if len(out.CapabilityTags) == 0 {
		return nil, fmt.Errorf("enrich: empty capability_tags")
	}
	if len(out.CanonicalInputs) == 0 {
		out.CanonicalInputs = json.RawMessage(`[]`)
	}
	if len(out.CanonicalOutputs) == 0 {
		out.CanonicalOutputs = json.RawMessage(`[]`)
	}
	out.EnrichmentVersion = enrichmentVersion
	return &out, nil
}

func buildEnrichmentPrompt(in EnrichInput) string {
	var b strings.Builder
	b.WriteString(`You are enriching a service description for a task-to-service search index. `)
	b.WriteString(`Return ONLY valid JSON matching the schema below. No prose, no code fences.

Schema:
{
  "capability_tags":   ["..."],          // 3-10 free-form tags
  "use_cases":         "...",            // 200-500 chars, buyer perspective starting with "Use when..."
  "canonical_inputs":  [{"name":"...","type":"..."}],
  "canonical_outputs": [{"name":"...","type":"..."}]
}

Service:
Title: `)
	b.WriteString(in.Title)
	b.WriteString("\nCapability: ")
	b.WriteString(in.CapabilityDesc)
	b.WriteString("\nCall spec: ")
	b.WriteString(in.CallSpecText)
	if in.CallSpecSchema != "" {
		b.WriteString("\nCall spec schema: ")
		b.WriteString(in.CallSpecSchema)
	}
	return b.String()
}

// stripCodeFence trims ```json ... ``` wrappers some LLMs add despite instructions.
func stripCodeFence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

func truncateForErr(s string) string {
	const n = 200
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
