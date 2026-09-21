package dispatch

import (
	"strings"
	"testing"
)

func TestParseDecisionStrictContract(t *testing.T) {
	cases := []struct {
		name, raw, request string
		valid              bool
	}{
		{"reply", `{"version":1,"request_id":"job-a","action":"reply","reply_text":"你好"}`, "job-a", true},
		{"no reply", `{"version":1,"request_id":"job-a","action":"no_reply","reply_text":""}`, "job-a", true},
		{"needs user", `{"version":1,"request_id":"job-a","action":"needs_user","reply_text":""}`, "job-a", true},
		{"missing reply text", `{"version":1,"request_id":"job-a","action":"needs_user"}`, "job-a", false},
		{"null reply text", `{"version":1,"request_id":"job-a","action":"needs_user","reply_text":null}`, "job-a", false},
		{"wrong request", `{"version":1,"request_id":"job-b","action":"no_reply"}`, "job-a", false},
		{"missing request", `{"version":1,"action":"no_reply"}`, "", false},
		{"version", `{"version":2,"request_id":"job-a","action":"no_reply"}`, "job-a", false},
		{"unknown field", `{"version":1,"request_id":"job-a","action":"no_reply","send":true}`, "job-a", false},
		{"duplicate action", `{"version":1,"request_id":"job-a","action":"no_reply","action":"reply","reply_text":"hidden"}`, "job-a", false},
		{"case variant action", `{"version":1,"request_id":"job-a","action":"no_reply","Action":"reply","reply_text":"hidden"}`, "job-a", false},
		{"duplicate request", `{"version":1,"request_id":"other","request_id":"job-a","action":"no_reply"}`, "job-a", false},
		{"second object", `{"version":1,"request_id":"job-a","action":"no_reply"} {}`, "job-a", false},
		{"null", `null`, "job-a", false},
		{"array", `[]`, "job-a", false},
		{"markdown", "```json\n{}\n```", "job-a", false},
		{"invalid utf8", string([]byte{0xff}), "job-a", false},
		{"blank reply", `{"version":1,"request_id":"job-a","action":"reply","reply_text":" \n"}`, "job-a", false},
		{"bad Unicode", `{"version":1,"request_id":"job-a","action":"reply","reply_text":"\ud800"}`, "job-a", false},
		{"reply with no reply", `{"version":1,"request_id":"job-a","action":"no_reply","reply_text":"send this"}`, "job-a", false},
		{"unsupported action", `{"version":1,"request_id":"job-a","action":"send"}`, "job-a", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseDecision(tc.raw, tc.request)
			if (err == nil) != tc.valid {
				t.Fatalf("ParseDecision error=%v valid=%v", err, tc.valid)
			}
		})
	}
}

func TestParseDecisionSizeLimit(t *testing.T) {
	raw := `{"version":1,"request_id":"job","action":"no_reply","reply_text":""}`
	if _, err := ParseDecision(raw+strings.Repeat(" ", (1<<20)-len(raw)), "job"); err != nil {
		t.Fatalf("limit-sized valid result: %v", err)
	}
	if _, err := ParseDecision(raw+strings.Repeat(" ", (1<<20)+1-len(raw)), "job"); err == nil {
		t.Fatal("oversized result accepted")
	}
}
