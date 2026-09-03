package api

import (
	"encoding/json"
	"testing"
)

func TestAgentSettingsWriteDiscardsLegacySecurityBoundaryFields(t *testing.T) {
	for _, field := range []string{"recurring_publish", "auto_reply_pm", "auto_comment", "show_add_friend"} {
		var body agentSettingsWriteRequest
		if err := json.Unmarshal([]byte(`{"lang":"en","`+field+`":true}`), &body); err != nil {
			t.Fatal(err)
		}
		body.discardLegacySecurityBoundary()
		if body.Lang == nil || *body.Lang != "en" {
			t.Fatalf("ordinary setting was discarded for %q", field)
		}
		if body.RecurringPublish != nil || body.AutoReplyPM != nil || body.AutoComment != nil || body.ShowAddFriend != nil {
			t.Fatalf("legacy security field %q was not discarded", field)
		}
	}
}
