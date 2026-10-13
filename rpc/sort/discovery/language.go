package discovery

import (
	"strings"

	"golang.org/x/text/language"
)

// Historical Cards store these display names as well as standard language codes.
// This is a bounded read compatibility table, not a natural-language parser.
var languageNames = []struct{ name, code string }{
	{"english", "en"},
	{"chinese", "zh"},
	{"中文", "zh"},
}

// languageKey folds ASCII case in recognized codes without dropping subtags or
// rewriting deprecated codes. Unknown/composite evidence keeps exact equality.
func languageKey(value string) (string, bool) {
	lower := strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, value)
	for _, alias := range languageNames {
		if lower == alias.name {
			return alias.code, true
		}
	}
	for _, r := range value {
		if r != '-' && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return value, false
		}
	}
	tag, err := language.Parse(value)
	base, _, _ := tag.Raw()
	if err != nil || base.String() == "und" {
		return value, false
	}
	return lower, true
}

func equalLanguage(a, b string) bool {
	x, _ := languageKey(a)
	y, _ := languageKey(b)
	return x == y
}

func intersectsLanguages(a, b []string) bool {
	keys := make(map[string]bool, len(a))
	for _, x := range a {
		key, _ := languageKey(x)
		keys[key] = true
	}
	for _, y := range b {
		key, _ := languageKey(y)
		if keys[key] {
			return true
		}
	}
	return false
}

// languageFilter matches the same equivalence classes as the authority check.
// ASCII case-insensitive keyword terms retain exact locale boundaries and also
// cover mixed-case historical names without changing ES documents or mappings.
func languageFilter(field string, values []string) map[string]any {
	clauses := []any{}
	seen := map[string]bool{}
	unknown := []string{}
	for _, value := range values {
		key, recognized := languageKey(value)
		if !recognized {
			unknown = appendUnique(unknown, value)
			continue
		}
		variants := []string{key}
		for _, alias := range languageNames {
			if alias.code == key {
				variants = append(variants, alias.name)
			}
		}
		for _, variant := range variants {
			if !seen[variant] {
				clauses = append(clauses, map[string]any{"term": map[string]any{field: map[string]any{"value": variant, "case_insensitive": true}}})
				seen[variant] = true
			}
		}
	}
	if len(unknown) > 0 {
		clauses = append(clauses, terms(field, unknown))
	}
	if len(clauses) == 1 {
		return clauses[0].(map[string]any)
	}
	return map[string]any{"bool": map[string]any{"should": clauses, "minimum_should_match": 1}}
}
