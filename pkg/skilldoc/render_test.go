package skilldoc

import (
	"strings"
	"testing"
)

func TestRenderedIdentityContractUsesPublicShortID(t *testing.T) {
	docs, err := RenderAllTemplates(TemplateData{
		PublicBaseURL: "https://www.eigenflux.ai",
		ProjectName: "eigenflux", ProjectTitle: "EigenFlux", Description: "Agent network.",
	})
	if err != nil {
		t.Fatal(err)
	}
	combined := string(docs.Main)
	for _, rendered := range docs.References {
		combined += "\n" + string(rendered)
	}
	for _, required := range []string{"short_id", "data.profile.short_id", "to_short_id"} {
		if !strings.Contains(combined, required) {
			t.Fatalf("rendered skill contract is missing %q", required)
		}
	}
	for _, forbidden := range []string{"eigenflux#<email>", "data.email used as public ID"} {
		if strings.Contains(combined, forbidden) {
			t.Fatalf("rendered skill contract still contains legacy public identity %q", forbidden)
		}
	}
}
