package discovery

import (
	"strings"
	"unicode"
)

const LexicalEvidenceV1 = "broadcast_phrase_evidence_v1"
const lexicalEvidenceSalt = "discovery-lexical-evidence-v1:"

// hanPairs retains positional evidence rather than isolated Han characters.
// Bound the query size with evenly spaced samples of distinct pairs, so long
// context text cannot create an unbounded Boolean query or favor only its prefix.
func hanPairs(text string) []string {
	seen := map[string]bool{}
	pairs := []string{}
	var previous rune
	for _, r := range []rune(text) {
		if unicode.Is(unicode.Han, previous) && unicode.Is(unicode.Han, r) {
			pair := string([]rune{previous, r})
			if !seen[pair] {
				seen[pair] = true
				pairs = append(pairs, pair)
			}
		}
		previous = r
	}
	const limit = 64
	if len(pairs) <= limit {
		return pairs
	}
	sampled := make([]string, limit)
	for i := range sampled {
		sampled[i] = pairs[i*(len(pairs)-1)/(limit-1)]
	}
	return sampled
}

func lexicalPhraseEvidence(c Context, fields []string) any {
	pairs := hanPairs(c.lexicalQuery())
	if len(pairs) == 0 {
		return nil
	}
	clauses := make([]any, 0, len(pairs))
	for _, pair := range pairs {
		clauses = append(clauses, map[string]any{"multi_match": map[string]any{"query": pair, "fields": fields, "type": "phrase"}})
	}
	// This is a lexical evidence gate, not a claim of semantic entailment.
	// The quarter-coverage policy must pass real-context replay before activation.
	return map[string]any{"bool": map[string]any{"should": clauses, "minimum_should_match": (len(pairs) + 3) / 4}}
}

// Topic labels are supplemental evidence for short Latin topic queries, not
// generic matches for every occurrence of "AI" in a long Need description.
func shortLatinTopic(text string) bool {
	for _, r := range text {
		if !(unicode.Is(unicode.Latin, r) || unicode.IsDigit(r) || unicode.IsSpace(r) || r == '-' || r == '_') {
			return false
		}
	}
	words := strings.FieldsFunc(text, func(r rune) bool { return unicode.IsSpace(r) || r == '-' || r == '_' })
	return len(words) > 0 && len(words) <= 4
}
