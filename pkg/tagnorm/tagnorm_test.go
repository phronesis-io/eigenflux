package tagnorm

import "testing"

func TestNormalize(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// separator convention folds onto one canonical form
		{"ai agents", "aiagents"},
		{"ai-agents", "aiagents"},
		{"ai_agents", "aiagents"},
		{"AI Agents", "aiagents"},
		// hyphenated-on-both-sides terms still collapse consistently
		{"e-commerce", "ecommerce"},
		{"E-Commerce", "ecommerce"},
		{"a-share", "ashare"},
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

// TestNormalizeFoldsConventions is the property the whole change relies on:
// the extractor's hyphenated form and the item tagger's spaced form must land
// on the same key.
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
