// Package tagnorm canonicalizes keyword/domain tags so that exact-overlap
// matching is agnostic to separator convention.
//
// The two tag vocabularies on the network disagree on how to join multi-word
// terms: the item tagger emits space-separated phrases ("ai agents", "market
// data") while the profile keyword extractor emits hyphenated compounds
// ("ai-agents", "market-data"). Both sides are also internally inconsistent
// (e.g. items carry "open-source" and "a-share" hyphenated). A plain
// lowercase exact match therefore only ever fires on single-word tags, which
// is why most multi-word beats score zero on the Coverage view.
//
// Stripping separators before comparing folds every convention onto one
// canonical form ("ai agents", "ai-agents", "aiagents" -> "aiagents"), which
// maximizes recall without losing the hyphenated-on-both-sides terms. See the
// beat/Coverage matching in api/dal/console.go and the ranking keyword-overlap
// signal in rpc/sort/ranker/signals.go.
package tagnorm

import "strings"

// sepStripper removes the separators that differ between the two vocabularies
// (space, hyphen, underscore). A Replacer is safe for concurrent use, so the
// per-item ranking hot path can share this single package-level instance.
var sepStripper = strings.NewReplacer(" ", "", "-", "", "_", "")

// Normalize returns the canonical form of a tag for separator-agnostic
// exact-overlap matching: lowercased, whitespace-trimmed, with spaces,
// hyphens and underscores removed. "AI Agents", "ai-agents" and "ai_agents"
// all normalize to "aiagents". An empty or separator-only input yields "".
func Normalize(s string) string {
	return sepStripper.Replace(strings.ToLower(strings.TrimSpace(s)))
}
