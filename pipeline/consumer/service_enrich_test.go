package consumer

import (
	"context"
	"errors"
	"testing"
)

type fakeLLM struct {
	response string
	err      error
	seen     string
}

func (f *fakeLLM) Complete(_ context.Context, prompt string) (string, error) {
	f.seen = prompt
	return f.response, f.err
}

func TestEnrichService_HappyPath(t *testing.T) {
	llm := &fakeLLM{response: `{
        "capability_tags": ["translate:es-zh","language:spanish"],
        "use_cases": "Use when you need to translate Spanish text into Chinese.",
        "canonical_inputs":  [{"name":"text","type":"string"}],
        "canonical_outputs": [{"name":"translated","type":"string"}]
    }`}
	out, err := EnrichService(context.Background(), llm, EnrichInput{
		Title:          "ES->ZH translator",
		CapabilityDesc: "translates Spanish text into Chinese",
		CallSpecText:   "{lang_pair: 'es-zh'}",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out.CapabilityTags) != 2 {
		t.Errorf("CapabilityTags = %v", out.CapabilityTags)
	}
	if out.UseCases == "" {
		t.Errorf("UseCases empty")
	}
	if out.EnrichmentVersion != 1 {
		t.Errorf("EnrichmentVersion = %d, want 1", out.EnrichmentVersion)
	}
	if string(out.CanonicalInputs) == "" || string(out.CanonicalInputs) == "null" {
		t.Errorf("CanonicalInputs should round-trip JSON: %s", string(out.CanonicalInputs))
	}
}

func TestEnrichService_StripsCodeFence(t *testing.T) {
	llm := &fakeLLM{response: "```json\n{\"capability_tags\":[\"x\"],\"use_cases\":\"Use when needed.\"}\n```"}
	out, err := EnrichService(context.Background(), llm, EnrichInput{Title: "t"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out.UseCases != "Use when needed." {
		t.Errorf("got %q", out.UseCases)
	}
}

func TestEnrichService_BadJSON(t *testing.T) {
	llm := &fakeLLM{response: "not json"}
	_, err := EnrichService(context.Background(), llm, EnrichInput{Title: "x"})
	if err == nil {
		t.Fatalf("want error for malformed JSON")
	}
}

func TestEnrichService_LLMError(t *testing.T) {
	llm := &fakeLLM{err: errors.New("upstream down")}
	_, err := EnrichService(context.Background(), llm, EnrichInput{Title: "x"})
	if err == nil {
		t.Fatalf("want error when LLM fails")
	}
}

func TestEnrichService_RejectsEmptyTags(t *testing.T) {
	llm := &fakeLLM{response: `{"use_cases":"Use when needed.","capability_tags":[]}`}
	_, err := EnrichService(context.Background(), llm, EnrichInput{Title: "t"})
	if err == nil {
		t.Fatalf("want error on empty capability_tags")
	}
}

func TestEnrichService_RejectsEmptyUseCases(t *testing.T) {
	llm := &fakeLLM{response: `{"capability_tags":["x"]}`}
	_, err := EnrichService(context.Background(), llm, EnrichInput{Title: "t"})
	if err == nil {
		t.Fatalf("want error on empty use_cases")
	}
}
