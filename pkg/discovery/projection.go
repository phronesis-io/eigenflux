package discovery

import "eigenflux_server/pkg/taxonomy"

// ContentSlots maps existing source-owned labels by exact canonical ID/name or
// reviewed alias. Ambiguous branches remain unknown; free-form content and
// private owner geography are never promoted into provider evidence.
func ContentSlots(v *taxonomy.Vocabulary, labels, languages []string) Slots {
	out := Slots{Lang: append([]string(nil), languages...)}
	if v == nil {
		return out
	}
	out.TaxonomyVersion = v.Version
	categories, subtypes := map[string]bool{}, map[string]bool{}
	matches := func(n taxonomy.Node) bool {
		for _, s := range append([]string{n.ID, n.Name}, n.Aliases...) {
			for _, l := range labels {
				if Normalize(s) == Normalize(l) {
					return true
				}
			}
		}
		return false
	}
	for _, n := range v.Categories {
		if matches(n) {
			categories[n.ID] = true
		}
	}
	for _, n := range v.Subtypes {
		if matches(n) {
			categories[n.Category] = true
			subtypes[n.ID] = true
		}
	}
	for _, n := range v.Intents {
		if matches(n) {
			categories[n.Category] = true
			if n.Subtype != "" {
				subtypes[n.Subtype] = true
			}
			out.Intents = append(out.Intents, n.ID)
		}
	}
	if len(categories) == 1 {
		for k := range categories {
			out.Category = k
		}
		if len(subtypes) == 1 {
			for k := range subtypes {
				out.Subtype = k
			}
		}
	}
	return out
}
