package dispatch

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestWorkBuddyJSONFenceBoundary(t *testing.T) {
	object := "{\"summary\":\"a ``` fence inside JSON\"}"
	for _, tt := range []struct{ name, raw, want string }{
		{"json fence", "```json\n" + object + "\n```", object},
		{"plain fence", "```\n" + object + "\n```", object},
		{"outer whitespace", " \n```json\n" + object + "\n```\n", object},
		{"raw object", object, object},
		{"leading prose", "result:\n```json\n{}\n```", ""},
		{"trailing prose", "```json\n{}\n```\ndone", ""},
		{"multiple blocks", "```json\n{}\n```\n```json\n{}\n```", ""},
		{"second object", "```json\n{} {}\n```", ""},
		{"invalid object", "```json\n{\n```", ""},
		{"array", "```json\n[]\n```", ""},
		{"null", "```json\nnull\n```", ""},
		{"other language", "```javascript\n{}\n```", ""},
		{"missing newline", "```json{} ```", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			want := tt.want
			if want == "" {
				want = tt.raw
			}
			if got := unwrapWorkBuddyJSONFence(tt.raw); got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
		})
	}
}

func TestWorkBuddyACPFenceKeepsStrictBusinessValidation(t *testing.T) {
	valid := validCommissionResult("request", "ready")
	raw, _ := json.Marshal(valid)
	for _, tt := range []struct {
		name, host, body string
		valid            bool
	}{
		{"workbuddy", "workbuddy", string(raw), true},
		{"other ACP", "hermes", string(raw), false},
		{"duplicate field", "workbuddy", strings.TrimSuffix(string(raw), "}") + `,"outcome":"ready"}`, false},
		{"unknown field", "workbuddy", strings.TrimSuffix(string(raw), "}") + `,"accept":true}`, false},
		{"wrong request", "workbuddy", strings.Replace(string(raw), `"request"`, `"different"`, 1), false},
		{"wrong version", "workbuddy", strings.Replace(string(raw), `"order_version":4`, `"order_version":5`, 1), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			b := fixtureBinding(t, "acp", "acp-json-result")
			b.Host = tt.host
			b.Env["EF_ACP_RESULT"] = "```json\n" + tt.body + "\n```"
			result, err := (Runner{Binding: b}).Run(context.Background(), Request{Prompt: "inspect"})
			if err != nil {
				t.Fatal(err)
			}
			_, err = ParseCommissionIntakeDecision(result.Text, "request", valid.OrderID, 4)
			if (err == nil) != tt.valid {
				t.Fatalf("validation=%v, want valid=%v, result=%q", err, tt.valid, result.Text)
			}
			if tt.host == "hermes" && !strings.HasPrefix(result.Text, "```json") {
				t.Fatal("other host result changed")
			}
		})
	}
}
