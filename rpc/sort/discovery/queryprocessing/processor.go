// Package queryprocessing prepares retrieval text independently of its input source.
package queryprocessing

import (
	searchindex "eigenflux_server/rpc/sort/discovery/index"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxExpansions = 8

// Analysis is frozen with the context and samples. Query itself retains
// the caller's wording; expansions never replace it or become hard filters.
type Analysis struct {
	Script     string      `json:"script"`
	Ambiguous  []string    `json:"ambiguous,omitempty"`
	Phrases    []string    `json:"phrases,omitempty"`
	Version    string      `json:"version"`
	Normalized string      `json:"normalized"`
	Identity   bool        `json:"identity"`
	Intents    []string    `json:"intents,omitempty"`
	Expansions []Expansion `json:"expansions,omitempty"`
}

type Expansion struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Query    string `json:"query"`
	IntentID string `json:"intent_id"`
}

// Options bounds alias matching and protects exact identity queries.
type Options struct {
	Category string
	Subtype  string
	Identity bool
}

// Process preserves original wording outside its result, never changes hard
// filters, and emits bounded retrieval evidence. Vocabulary may be nil when no
// reviewed aliases are configured.
func Process(query string, v *searchindex.Vocabulary, options Options) *Analysis {
	p := &Analysis{Version: "query_rules_v1", Normalized: strings.TrimSpace(query), Identity: options.Identity}
	if options.Identity {
		p.Script = "identity"
		return p
	}
	p.Normalized = searchindex.Normalize(query)
	p.Script = queryScript(p.Normalized)
	if p.Script == "cjk" && utf8.RuneCountInString(p.Normalized) <= 12 && !strings.Contains(p.Normalized, " ") {
		p.Phrases = append(p.Phrases, p.Normalized)
	}
	// Only reviewed intent names/aliases in the requested branch participate.
	// A label shared by different intents is ambiguous and cannot be expanded.
	if v == nil {
		return p
	}
	owners := map[string]string{}
	labels := map[string][]string{}
	for _, n := range v.Intents {
		if options.Category != "" && n.Category != options.Category || options.Subtype != "" && n.Subtype != options.Subtype {
			continue
		}
		for _, raw := range append([]string{n.Name}, n.Aliases...) {
			label := searchindex.Normalize(raw)
			if size := utf8.RuneCountInString(label); size < 2 || size > 100 {
				continue
			}
			if owner, exists := owners[label]; exists && owner != n.ID {
				owners[label] = ""
			} else if !exists {
				owners[label] = n.ID
			}
			labels[n.ID] = appendUnique(labels[n.ID], label)
		}
	}
	ordered := make([]string, 0, len(owners))
	for label := range owners {
		ordered = append(ordered, label)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if len(ordered[i]) != len(ordered[j]) {
			return len(ordered[i]) > len(ordered[j])
		}
		return ordered[i] < ordered[j]
	})
	// Prefer longer phrases and never expand a shorter overlapping phrase.
	used := make([]bool, len(p.Normalized))
	queries := map[string]bool{}
	for _, from := range ordered {
		id := owners[from]
		start, end := findQueryPhrase(p.Normalized, from)
		if start < 0 {
			continue
		}
		if id == "" {
			if len(p.Ambiguous) < 5 {
				p.Ambiguous = appendUnique(p.Ambiguous, from)
			}
			continue
		}
		overlaps := false
		for _, taken := range used[start:end] {
			overlaps = overlaps || taken
		}
		if overlaps {
			continue
		}
		for i := start; i < end; i++ {
			used[i] = true
		}
		if script := queryScript(from); script == "cjk" || script == "mixed" {
			if len(p.Phrases) < 5 {
				p.Phrases = appendUnique(p.Phrases, from)
			}
		}
		if len(p.Intents) < 5 {
			p.Intents = appendUnique(p.Intents, id)
		}
		targets := append([]string(nil), labels[id]...)
		sort.Strings(targets)
		for _, to := range targets {
			present, _ := findQueryPhrase(p.Normalized, to)
			if owners[to] != id || to == from || present >= 0 {
				continue
			}
			// One substitution per variant preserves all other query terms,
			// including negatives. There is no recursive synonym expansion.
			expanded := p.Normalized[:start] + to + p.Normalized[end:]
			if !queries[expanded] && len(p.Expansions) < MaxExpansions {
				p.Expansions = append(p.Expansions, Expansion{From: from, To: to, Query: expanded, IntentID: id})
				queries[expanded] = true
			}
		}
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

// Query aliases can touch CJK text without spaces. Latin edges still require
// word boundaries: k8s in 擅长k8s运维 is valid, art in partial is not.
// Hard exclude phrases retain their separate, existing matching contract.
func findQueryPhrase(text, phrase string) (int, int) {
	if phrase == "" {
		return -1, -1
	}
	first, _ := utf8.DecodeRuneInString(phrase)
	last, _ := utf8.DecodeLastRuneInString(phrase)
	word := func(r rune) bool { return !isCJK(r) && (unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_') }
	for start := 0; start <= len(text); {
		i := strings.Index(text[start:], phrase)
		if i < 0 {
			break
		}
		i += start
		end := i + len(phrase)
		before, _ := utf8.DecodeLastRuneInString(text[:i])
		after, _ := utf8.DecodeRuneInString(text[end:])
		if (isCJK(first) || i == 0 || !word(before)) && (isCJK(last) || end == len(text) || !word(after)) {
			return i, end
		}
		start = i + 1
	}
	return -1, -1
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
