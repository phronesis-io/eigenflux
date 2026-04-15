package audience

import "testing"

func TestValidate_Valid(t *testing.T) {
	for _, expr := range []string{"", "skill_ver_num < 3", `skill_ver == "0.0.3"`, "cli_ver_num >= 10200", `cli_ver == "1.2.0"`, "agent_id == 123", `email == "test@example.com"`} {
		if err := Validate(expr); err != nil {
			t.Fatalf("Validate(%q) unexpected error: %v", expr, err)
		}
	}
}

func TestValidate_UnknownVariable(t *testing.T) {
	if err := Validate("foo_bar == 1"); err == nil {
		t.Fatal("expected error for unknown variable")
	}
}

func TestValidate_InvalidSyntax(t *testing.T) {
	if err := Validate("skill_ver_num <><> 3"); err == nil {
		t.Fatal("expected error for invalid syntax")
	}
}
