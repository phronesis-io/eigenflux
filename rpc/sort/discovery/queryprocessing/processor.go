// Package queryprocessing prepares retrieval text independently of its input source.
package queryprocessing

import (
	searchindex "eigenflux_server/rpc/sort/discovery/index"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Version changes whenever processed embedding input semantics change.
const Version = "query_rules_v2"

// NeedText is shared by online compilation and asynchronous precomputation.
func NeedText(goal, context string) string { return strings.TrimSpace(goal + "\n" + context) }

// Analysis is frozen with the context and samples. Query itself retains
// the caller's wording; processing never introduces hard filters.
type Analysis struct {
	Script     string   `json:"script"`
	Phrases    []string `json:"phrases,omitempty"`
	Version    string   `json:"version"`
	Normalized string   `json:"normalized"`
	Identity   bool     `json:"identity"`
}

// Options protects exact identity queries.
type Options struct {
	Identity bool
}

// Process preserves original wording outside its result and never changes hard filters.
func Process(query string, options Options) *Analysis {
	p := &Analysis{Version: Version, Normalized: strings.TrimSpace(query), Identity: options.Identity}
	if options.Identity {
		p.Script = "identity"
		return p
	}
	p.Normalized = searchindex.Normalize(query)
	p.Script = queryScript(p.Normalized)
	if p.Script == "cjk" && utf8.RuneCountInString(p.Normalized) <= 12 && !strings.Contains(p.Normalized, " ") {
		p.Phrases = append(p.Phrases, p.Normalized)
	}
	return p
}

func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r)
}

// This is script detection, not a language filter or a translation decision.
func queryScript(text string) string {
	latin, cjk := false, false
	for _, r := range text {
		latin = latin || unicode.Is(unicode.Latin, r)
		cjk = cjk || isCJK(r)
	}
	if latin && cjk {
		return "mixed"
	}
	if cjk {
		return "cjk"
	}
	if latin {
		return "latin"
	}
	return "other"
}
