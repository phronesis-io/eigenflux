package requestcontract

import (
	"strings"
	"testing"
)

func TestValidateSpec(t *testing.T) {
	for _, tt := range []struct {
		name, text, schema string
		invalid            bool
	}{
		{"canonical", "Provide a UTF-8 JSON object at inputs/request.txt.", `{"type":"object"}`, false},
		{"markdown and attachments", "Upload `inputs/request.txt` and inputs/data.json", `{"required":["scenario"]}`, false},
		{"Chinese punctuation", "上传 inputs/request.txt。", `{"type":"object"}`, false},
		{"wrong path", "Provide JSON at inputs/request.json.", `{"required":["scenario"]}`, true},
		{"both paths", "Upload inputs/request.txt and inputs/request.json", `{"type":"object"}`, true},
		{"undeclared", "Provide a JSON request", `{"type":"object"}`, true},
		{"prefixed", "Upload xinputs/request.txt", `{"type":"object"}`, true},
		{"nested", "Upload nested/inputs/request.txt", `{"type":"object"}`, true},
		{"suffix", "Upload inputs/request.txt.backup", `{"type":"object"}`, true},
		{"child", "Upload inputs/request.txt/file", `{"type":"object"}`, true},
		{"unconstrained", "Provide inputs/request.json", `{}`, false},
		{"empty legacy schema", "Provide any attachment", "", false},
		{"null schema", "Upload inputs/request.txt", `null`, true},
		{"array schema", "Upload inputs/request.txt", `[]`, true},
		{"malformed schema", "Upload inputs/request.txt", `{`, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSpec(tt.text, tt.schema)
			if (err != nil) != tt.invalid {
				t.Fatalf("ValidateSpec() = %v; invalid=%v", err, tt.invalid)
			}
			if tt.invalid && !strings.Contains(err.Error(), Path) && !strings.Contains(err.Error(), "schema") {
				t.Fatalf("missing actionable path/schema error: %v", err)
			}
		})
	}
}
