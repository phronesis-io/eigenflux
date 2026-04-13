package audience

import (
	"testing"
)

func TestEvaluate_EmptyExpression(t *testing.T) {
	ok, err := Evaluate("", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("empty expression should return true")
	}
}

func TestEvaluate_SkillVerNum(t *testing.T) {
	tests := []struct {
		name string
		expr string
		vars map[string]string
		want bool
	}{
		{"less than match", "skill_ver_num < 3", map[string]string{"skill_ver_num": "2"}, true},
		{"less than no match", "skill_ver_num < 3", map[string]string{"skill_ver_num": "3"}, false},
		{"less than large", "skill_ver_num < 3", map[string]string{"skill_ver_num": "100"}, false},
		{"no header defaults 0", "skill_ver_num < 3", map[string]string{}, true},
		{"compound no header", "skill_ver_num > 0 && skill_ver_num < 3", map[string]string{}, false},
		{"gte match", "skill_ver_num >= 3", map[string]string{"skill_ver_num": "3"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Evaluate(tt.expr, tt.vars)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("Evaluate(%q) = %v, want %v", tt.expr, got, tt.want)
			}
		})
	}
}

func TestEvaluate_SkillVerString(t *testing.T) {
	ok, err := Evaluate(`skill_ver == "0.0.3"`, map[string]string{"skill_ver": "0.0.3"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected true")
	}
}

func TestEvaluate_AgentID(t *testing.T) {
	tests := []struct {
		name string
		expr string
		vars map[string]string
		want bool
	}{
		{"match", "agent_id == 123", map[string]string{"agent_id": "123"}, true},
		{"no match", "agent_id == 123", map[string]string{"agent_id": "456"}, false},
		{"missing defaults 0", "agent_id == 0", map[string]string{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Evaluate(tt.expr, tt.vars)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("Evaluate(%q) = %v, want %v", tt.expr, got, tt.want)
			}
		})
	}
}

func TestEvaluate_InvalidExpression(t *testing.T) {
	ok, err := Evaluate("invalid !!!", map[string]string{})
	if err == nil {
		t.Fatal("expected error for invalid expression")
	}
	if ok {
		t.Fatal("expected false for invalid expression")
	}
}

func TestValidate_Valid(t *testing.T) {
	for _, expr := range []string{"", "skill_ver_num < 3", `skill_ver == "0.0.3"`, "agent_id == 123", "cli_ver_num < 100", `cli_ver == "0.1.0"`} {
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

func TestEvaluate_CLIVerNum(t *testing.T) {
	tests := []struct {
		name string
		expr string
		vars map[string]string
		want bool
	}{
		{"less than match", "cli_ver_num < 100", map[string]string{"cli_ver_num": "1"}, true},
		{"less than no match", "cli_ver_num < 100", map[string]string{"cli_ver_num": "100"}, false},
		{"no header defaults 0", "cli_ver_num < 100", map[string]string{}, true},
		{"gte match", "cli_ver_num >= 100", map[string]string{"cli_ver_num": "200"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Evaluate(tt.expr, tt.vars)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("Evaluate(%q) = %v, want %v", tt.expr, got, tt.want)
			}
		})
	}
}

func TestEvaluate_CLIVerString(t *testing.T) {
	ok, err := Evaluate(`cli_ver == "0.1.0"`, map[string]string{"cli_ver": "0.1.0"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected true")
	}
}

func TestEvaluate_CompoundSkillAndCLI(t *testing.T) {
	ok, err := Evaluate(`skill_ver_num >= 3 || cli_ver_num >= 100`, map[string]string{"cli_ver_num": "100"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected true when cli_ver matches")
	}
}

func TestEvaluate_SkillVerNoHeader(t *testing.T) {
	ok, err := Evaluate(`skill_ver != ""`, map[string]string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected false for empty skill_ver")
	}
}
