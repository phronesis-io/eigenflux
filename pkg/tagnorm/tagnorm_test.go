package tagnorm

import "testing"

func TestNormalize(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// separator convention folds onto one canonical form (single space)
		{"ai agents", "ai agents"},
		{"ai-agents", "ai agents"},
		{"ai_agents", "ai agents"},
		{"AI Agents", "ai agents"},
		{"ai   agents", "ai agents"}, // whitespace runs collapse
		// hyphenated-on-both-sides terms fold consistently
		{"e-commerce", "e commerce"},
		{"E-Commerce", "e commerce"},
		{"a-share", "a share"},
		// single-word and edge cases
		{"defi", "defi"},
		{"  MCP  ", "mcp"},
		{"golang", "golang"},
		{"", ""},
		{"-", ""},
		{"  -  ", ""},
	}
	for _, c := range cases {
		if got := Normalize(c.in); got != c.want {
			t.Errorf("Normalize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The property the whole change relies on: the extractor's hyphenated form and
// the item tagger's spaced form must land on the same key.
func TestNormalizeFoldsConventions(t *testing.T) {
	pairs := [][2]string{
		{"ai-agents", "ai agents"},
		{"market-data", "market data"},
		{"multi-agent-architecture", "multi agent architecture"},
		{"prediction-markets", "prediction markets"},
	}
	for _, p := range pairs {
		if Normalize(p[0]) != Normalize(p[1]) {
			t.Errorf("Normalize(%q)=%q != Normalize(%q)=%q", p[0], Normalize(p[0]), p[1], Normalize(p[1]))
		}
	}
}

// Folding is minimal: it must NOT merge across word boundaries, so genuinely
// different concepts that differ only by the presence/absence of a boundary
// stay distinct.
func TestNormalizePreservesWordBoundaries(t *testing.T) {
	distinct := [][2]string{
		{"co op", "coop"},
		{"ai infrastructure", "infrastructure"},
		{"e commerce", "ecommerce"},
	}
	for _, p := range distinct {
		if Normalize(p[0]) == Normalize(p[1]) {
			t.Errorf("Normalize(%q) and Normalize(%q) should differ, both=%q", p[0], p[1], Normalize(p[0]))
		}
	}
}
