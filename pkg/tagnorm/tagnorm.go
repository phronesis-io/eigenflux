// Package tagnorm canonicalizes keyword/domain tags so that exact-overlap
// matching is agnostic to separator convention.
//
// The two tag vocabularies on the network disagree on how to join multi-word
// terms: the item tagger emits space-separated phrases ("ai agents", "market
// data") while the profile keyword extractor emits hyphenated compounds
// ("ai-agents", "market-data"). Both sides are also internally inconsistent
// (items carry "open-source" and "a-share" hyphenated). A plain lowercase exact
// match therefore only fires on single-word tags, so most multi-word beats
// score zero on the Coverage view.
//
// Normalize folds the separator conventions — hyphen, underscore and runs of
// whitespace all collapse to a single space — so "ai agents", "ai-agents" and
// "ai_agents" compare equal. It deliberately does NOT remove word boundaries:
// "co op" stays distinct from "coop", and "ai infrastructure" stays distinct
// from "infrastructure". This is the minimal folding needed to bridge the two
// separator conventions; it does not fold lexical synonyms, abbreviations, or
// non-ASCII separators (en-dash, NBSP). See the beat/Coverage matching in
// api/dal/console.go and the ranking keyword-overlap signal in
// rpc/sort/ranker/signals.go.
package tagnorm

import "strings"

// sepFolder rewrites the ASCII separator characters that differ between the two
// vocabularies (hyphen, underscore) to a plain space. A Replacer is safe for
// concurrent use, so the per-item ranking hot path can share this single
// package-level instance.
var sepFolder = strings.NewReplacer("-", " ", "_", " ")

// Normalize returns the canonical form of a tag for separator-agnostic
// exact-overlap matching: lowercased, with hyphens/underscores folded to spaces
// and any run of whitespace collapsed to a single space (leading/trailing
// removed). "AI-Agents", "ai agents" and "ai_agents" all normalize to
// "ai agents". Word boundaries are preserved, so "co op" != "coop". An empty or
// separator-only input yields "".
func Normalize(s string) string {
	return strings.Join(strings.Fields(sepFolder.Replace(strings.ToLower(s))), " ")
}
