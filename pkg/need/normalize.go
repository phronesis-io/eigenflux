package need

import (
	"regexp"
	"strings"

	"golang.org/x/text/language"
)

const (
	BasicNormalizerVersion      = "basic.v1"
	VocabularyNormalizerVersion = "basic.v1+vocabulary.v1"
	MappingUnmapped             = "unmapped"
	MappingPartial              = "partial"
	MappingMapped               = "mapped"
)

// NormalizeBasic is the entire online normalization path. It has no vocabulary,
// model, network, clock, or queue dependency. Validation errors remain errors.
func NormalizeBasic(in Input) (Normalized, error) {
	if err := Validate(in); err != nil {
		return Normalized{}, err
	}
	phrases := cleanDistinct(in.Target.CandidateNeeds)
	c := in.Constraints
	c.BudgetMaxFen = copyNumber(c.BudgetMaxFen)
	c.MaxDurationMS = copyNumber(c.MaxDurationMS)
	c.DeadlineMS = copyNumber(c.DeadlineMS)
	c.ExcludeTerms = cleanDistinct(c.ExcludeTerms)
	var unresolved UnresolvedConstraints
	c.Lang, unresolved.Lang = normalizeAlternatives(c.Lang, normalizeLanguage)
	c.ProviderRegion, unresolved.ProviderRegion = normalizeAlternatives(c.ProviderRegion, normalizeRegion)
	return Normalized{
		Desc: cleanText(in.Target.Desc), CandidateNeeds: phrases,
		Constraints: c, UnresolvedConstraints: unresolved,
	}, nil
}

func copyNumber(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cleanText(value string) string { return strings.Join(strings.Fields(value), " ") }

func cleanDistinct(values []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, value := range values {
		value = cleanText(value)
		if value != "" && !seen[value] {
			out = append(out, value)
			seen[value] = true
		}
	}
	return out
}

// An array represents alternatives. Resolving only some alternatives would
// silently narrow the request, so defer the entire dimension if any are unknown.
func normalizeAlternatives(values []string, resolve func(string) (string, bool)) ([]string, []string) {
	original := cleanDistinct(values)
	canonical := []string{}
	for _, value := range original {
		resolved, ok := resolve(value)
		if !ok {
			return nil, original
		}
		canonical = append(canonical, resolved)
	}
	return cleanDistinct(canonical), nil
}

func normalizeLanguage(value string) (string, bool) {
	aliases := map[string]string{"中文": "zh", "英语": "en", "english": "en", "chinese": "zh", "日语": "ja", "japanese": "ja"}
	if alias, ok := aliases[strings.ToLower(value)]; ok {
		value = alias
	}
	tag, err := language.Parse(strings.ReplaceAll(value, "_", "-"))
	base, _, _ := tag.Raw()
	if err != nil || base.String() == "und" {
		return "", false
	}
	return tag.String(), true
}

func normalizeRegion(value string) (string, bool) {
	aliases := map[string]string{"中国": "CN", "china": "CN", "美国": "US", "united states": "US", "日本": "JP", "japan": "JP", "英国": "GB", "united kingdom": "GB", "uk": "GB"}
	if alias, ok := aliases[strings.ToLower(value)]; ok {
		value = alias
	}
	region, err := language.ParseRegion(strings.ToUpper(value))
	if err != nil || !region.IsCountry() {
		return "", false
	}
	return region.String(), true
}

// Vocabulary is an offline-produced immutable snapshot. Its version must change
// when any entry changes. Only exact, reviewed phrase-to-ID mappings are applied.
// Building, fetching and scheduling these snapshots is outside the online path.
type Vocabulary struct {
	Version string
	Needs   map[string]string
}

var canonicalNeedID = regexp.MustCompile(`^[a-z0-9][a-z0-9._:-]{0,127}$`)

// NormalizeWithVocabulary supplements the basic representation. Unknown phrases
// remain available to text retrieval; no hard constraint is inferred or changed.
func NormalizeWithVocabulary(in Input, vocabulary Vocabulary) (Normalized, error) {
	n, err := NormalizeBasic(in)
	if err != nil {
		return n, err
	}
	if !textValid(vocabulary.Version, 128, true) || strings.TrimSpace(vocabulary.Version) != vocabulary.Version {
		return Normalized{}, invalid("taxonomy_version", "required_version")
	}
	entries := make(map[string]string, len(vocabulary.Needs))
	for phrase, id := range vocabulary.Needs {
		key := cleanText(phrase)
		if !textValid(key, 200, true) || !canonicalNeedID.MatchString(id) {
			return Normalized{}, invalid("vocabulary", "invalid_mapping")
		}
		if previous, ok := entries[key]; ok && previous != id {
			return Normalized{}, invalid("vocabulary", "ambiguous_mapping")
		}
		entries[key] = id
	}
	n.MappedNeeds = map[string]string{}
	for _, phrase := range n.CandidateNeeds {
		if id, ok := entries[phrase]; ok {
			n.MappedNeeds[phrase] = id
		}
	}
	return n, nil
}
