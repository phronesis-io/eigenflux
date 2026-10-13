package index

// SlotsMapping is additive to existing content mappings. Unknown evidence is
// absent, never populated from the requesting owner's profile.
func SlotsMapping() map[string]any {
	p := map[string]any{}
	for _, k := range []string{"provider_region", "lang"} {
		p[k] = map[string]any{"type": "keyword"}
	}
	return map[string]any{"type": "object", "dynamic": "strict", "properties": p}
}
