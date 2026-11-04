package notificationpayload

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCommissionModerationPayload(t *testing.T) {
	original := CommissionModeration{NotificationVersion: 2, ModerationID: "9007199254740993", CommissionID: "9007199254740995", RecipientAgentID: "9007199254740997", DraftVersion: 2, Status: "reject", Decision: "reject", Flag: "claim", Field: "title", Reason: "请修改标题", OccurredAt: 1000}
	raw, _ := json.Marshal(original)
	parsed, err := ParseCommissionModeration(string(raw))
	if err != nil || parsed != original || !strings.Contains(parsed.Content(), original.Reason) || !strings.Contains(parsed.Content(), "reject") {
		t.Fatalf("parse: %#v %v", parsed, err)
	}
	for _, state := range []string{"pending", "review", "reject", "failed", "stale", "published", "pass"} {
		value := original
		value.Status = state
		switch state {
		case "pending", "failed", "stale":
			value.Decision = ""
		case "published", "pass":
			value.Decision = "pass"
		default:
			value.Decision = state
		}
		raw, _ := json.Marshal(value)
		if _, err := ParseCommissionModeration(string(raw)); err != nil {
			t.Fatalf("%s: %v", state, err)
		}
	}
	for name, change := range map[string]func(*CommissionModeration){
		"invalid ID":               func(p *CommissionModeration) { p.ModerationID = "0" },
		"noncanonical ID":          func(p *CommissionModeration) { p.CommissionID = "01" },
		"missing recipient":        func(p *CommissionModeration) { p.RecipientAgentID = "" },
		"bad notification version": func(p *CommissionModeration) { p.NotificationVersion = 0 },
		"bad version":              func(p *CommissionModeration) { p.DraftVersion = 0 },
		"bad time":                 func(p *CommissionModeration) { p.OccurredAt = 0 },
		"unknown status":           func(p *CommissionModeration) { p.Status = "processing" },
		"mismatched decision":      func(p *CommissionModeration) { p.Decision = "pass" },
		"empty reason":             func(p *CommissionModeration) { p.Reason = " " },
	} {
		t.Run(name, func(t *testing.T) {
			p := original
			change(&p)
			raw, _ := json.Marshal(p)
			if _, err := ParseCommissionModeration(string(raw)); err == nil {
				t.Fatal("invalid payload accepted")
			}
		})
	}
	if _, err := ParseCommissionModeration(strings.Replace(string(raw), `"flag":"claim"`, `"flag":null`, 1)); err == nil {
		t.Fatal("null flag accepted")
	}
	if _, err := ParseCommissionModeration(strings.Replace(string(raw), `"9007199254740993"`, `9007199254740993`, 1)); err == nil {
		t.Fatal("numeric ID accepted")
	}
}
