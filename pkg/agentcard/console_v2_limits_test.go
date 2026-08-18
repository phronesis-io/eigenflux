package agentcard

import (
	"strings"
	"testing"
)

func TestValidateConsoleV2ValueUsesUnicodeRuneLimits(t *testing.T) {
	if err := ValidateConsoleV2Value("agent_name", strings.Repeat("名", 40)); err != nil {
		t.Fatalf("exact limit rejected: %v", err)
	}
	if err := ValidateConsoleV2Value("agent_name", strings.Repeat("名", 41)); err == nil {
		t.Fatal("limit+1 was accepted")
	}
}

func TestValidateConsoleV2ValueAggregatesListCharacters(t *testing.T) {
	if err := ValidateConsoleV2Value("working_languages", []string{strings.Repeat("中", 50), strings.Repeat("文", 50)}); err != nil {
		t.Fatalf("exact aggregate limit rejected: %v", err)
	}
	if err := ValidateConsoleV2Value("working_languages", []interface{}{strings.Repeat("中", 50), strings.Repeat("文", 51)}); err == nil {
		t.Fatal("aggregate limit+1 was accepted")
	}
}

func TestValidateConsoleV2ValueLeavesLegacyOnlyFieldsUntouched(t *testing.T) {
	if err := ValidateConsoleV2Value("current_focus", strings.Repeat("x", 5000)); err != nil {
		t.Fatalf("non-V2 field should retain the legacy contract: %v", err)
	}
}
