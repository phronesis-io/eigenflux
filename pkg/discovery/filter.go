package discovery

import (
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
	"strconv"
	"strings"
	"unicode"
)

func Normalize(s string) string {
	return strings.Join(strings.Fields(cases.Fold().String(norm.NFKC.String(s))), " ")
}
func ContainsPhrase(text, phrase string) bool {
	text, phrase = Normalize(text), Normalize(phrase)
	if phrase == "" {
		return false
	}
	cjk := false
	for _, r := range phrase {
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) {
			cjk = true
		}
	}
	for start := 0; start <= len(text); {
		i := strings.Index(text[start:], phrase)
		if i < 0 {
			return false
		}
		i += start
		end := i + len(phrase)
		before := []rune(text[:i])
		after := []rune(text[end:])
		word := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_' }
		if cjk || (len(before) == 0 || !word(before[len(before)-1])) && (len(after) == 0 || !word(after[0])) {
			return true
		}
		start = i + 1
	}
	return false
}
func intersects(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}
func hasKind(a []Kind, k Kind) bool {
	for _, x := range a {
		if x == k {
			return true
		}
	}
	return false
}

// Check is the authoritative pure evaluator used after every recall channel
// and again with freshly hydrated source facts before serving.
func Check(c Context, d Document, mode Mode, now int64) string {
	if !c.Active(now) {
		return "inactive_context"
	}
	if !hasKind(c.Kinds, d.Ref.Type) {
		return "wrong_kind"
	}
	if !d.Ref.Type.Valid() || d.Ref.ID <= 0 || d.AuthorID <= 0 || !d.Visible || !d.Active {
		return "unavailable_source"
	}
	if d.AuthorID == c.OwnerID {
		return "self"
	}
	if d.Blocked {
		return "blocked"
	}
	if d.Ref.Type == Agent && mode == Recommendation && d.KnownContact {
		return "known_contact"
	}
	if d.ExpiresAt > 0 && d.ExpiresAt <= now {
		return "expired"
	}
	f := c.Filters
	for _, id := range f.ExcludeAuthors {
		if id == strconv.FormatInt(d.AuthorID, 10) {
			return "excluded_author"
		}
	}
	if f.Category != "" || f.Subtype != "" || len(f.Intents) > 0 {
		if f.TaxonomyVersion == "" || d.Slots.TaxonomyVersion != f.TaxonomyVersion {
			return "taxonomy_evidence"
		}
		if f.Category != "" && f.Category != d.Slots.Category {
			return "category"
		}
		if f.Subtype != "" && f.Subtype != d.Slots.Subtype {
			return "subtype"
		}
		if len(f.Intents) > 0 && !intersects(f.Intents, d.Slots.Intents) {
			return "intents"
		}
	}
	if len(f.Lang) > 0 && !intersects(f.Lang, d.Slots.Lang) {
		return "language"
	}
	if len(f.ProviderRegion) > 0 && !intersects(f.ProviderRegion, d.Slots.ProviderRegion) {
		return "provider_region"
	}
	if f.BudgetMaxFen != nil {
		if d.Ref.Type != Commission || d.PriceFen == nil || *d.PriceFen > *f.BudgetMaxFen || d.Currency == "" || d.Currency != f.Currency {
			return "budget"
		}
	}
	if f.Currency != "" && d.Currency != f.Currency {
		return "currency"
	}
	for _, bound := range []struct {
		min, max *int64
		value    *int64
		name     string
	}{{f.MinPriceFen, f.MaxPriceFen, d.PriceFen, "price"}, {f.MinDurationMS, f.MaxDurationMS, d.DurationMS, "duration"}} {
		if bound.min != nil || bound.max != nil {
			if d.Ref.Type != Commission || bound.value == nil || bound.min != nil && *bound.value < *bound.min || bound.max != nil && *bound.value > *bound.max {
				return bound.name
			}
		}
	}
	if f.DeadlineMS != nil && d.Ref.Type == Commission {
		if d.DurationMS == nil || *d.DurationMS <= 0 || *d.DurationMS > *f.DeadlineMS-now {
			return "deadline"
		}
	}
	for _, term := range f.ExcludeTerms {
		if d.Text == "" {
			return "missing_text"
		}
		if ContainsPhrase(d.Text, term) {
			return "excluded_term"
		}
	}
	return ""
}
