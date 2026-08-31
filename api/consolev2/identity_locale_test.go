package consolev2

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestIdentityAssertionIncludesEnglishDisplayName(t *testing.T) {
	encoded, err := json.Marshal(identityAssertion{
		SubjectType: "agent", SubjectID: "1", ShortID: "AbCdE",
		DisplayName: "星图研究助手", DisplayNameEn: "Atlas Research Assistant",
		VerificationLevel: "official",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"display_name_en":"Atlas Research Assistant"`) {
		t.Fatalf("English display name missing from identity assertion: %s", encoded)
	}
}
