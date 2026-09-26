package index

type Slots struct {
	Category        string   `json:"category,omitempty"`
	Subtype         string   `json:"subtype,omitempty"`
	Intents         []string `json:"intents,omitempty"`
	ProviderRegion  []string `json:"provider_region,omitempty"`
	Lang            []string `json:"lang,omitempty"`
	TaxonomyVersion string   `json:"taxonomy_version,omitempty"`
}
