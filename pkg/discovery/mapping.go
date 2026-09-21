package discovery

// SlotsMapping is additive to existing content mappings. Unknown evidence is
// absent, never populated from the requesting owner's profile.
func SlotsMapping() map[string]any {
	p := map[string]any{}
	for _, k := range []string{"category", "subtype", "intents", "provider_region", "lang", "taxonomy_version"} {
		p[k] = map[string]any{"type": "keyword"}
	}
	return map[string]any{"type": "object", "dynamic": "strict", "properties": p}
}
