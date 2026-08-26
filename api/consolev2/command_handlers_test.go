package consolev2

import (
	"encoding/json"
	"testing"
)

func TestLegacyAttentionCommandTypeIsNotValid(t *testing.T) {
	if validCommandType("attention_action") {
		t.Fatal("legacy Attention command type remained enabled")
	}
}

func TestValidateAttentionCommandResult(t *testing.T) {
	valid := []string{
		`{"summary":"已完成用户确认的动作。"}`,
		`{"summary":"已完成。","related_entities":[{"type":"broadcast","id":"1847926500138","url":"/dashboard/broadcasts/1847926500138","label":"查看广播"}]}`,
	}
	for _, raw := range valid {
		if err := validateAttentionCommandResult(json.RawMessage(raw)); err != nil {
			t.Fatalf("valid Attention result rejected: %v", err)
		}
	}

	invalid := []string{
		`{}`,
		`{"summary":""}`,
		`{"summary":"ok","private_body":"secret"}`,
		`{"summary":"ok","related_entities":[{"type":"unknown","id":"123"}]}`,
		`{"summary":"ok","related_entities":[{"type":"broadcast","id":""}]}`,
		`{"summary":"ok","related_entities":[{"type":"broadcast","id":"123","url":"https://example.com/path","trusted_public":true}]}`,
		`{"summary":"ok","related_entities":[{"type":"broadcast","id":"123","url":"http://127.0.0.1/private"}]}`,
		`{"summary":"ok","related_entities":[{"type":"broadcast","id":"123","url":"https://user:password@example.com/path"}]}`,
		`{"summary":"ok","related_entities":[{"type":"broadcast","id":"123","url":"//evil.example/path"}]}`,
		`{"summary":"ok","related_entities":[{"type":"broadcast","id":"123","url":"/\\evil.example/path"}]}`,
		"{\"summary\":\"ok\",\"related_entities\":[{\"type\":\"broadcast\",\"id\":\"123\",\"url\":\"/safe\u0001path\"}]}",
		`{"summary":"ok","related_entities":[{"type":"broadcast","id":"123","url":"/dashboard/handoff?ticket=secret"}]}`,
		`{"summary":"ok","related_entities":[{"type":"broadcast","id":"123","url":"/dashboard/item/123?session=secret"}]}`,
		`{"summary":"ok","related_entities":[{"type":"broadcast","id":"123","url":"/dashboard/item/456"}]}`,
		`{"summary":"ok","related_entities":[{"type":"broadcast","id":"123","url":"/dashboard/item/123"}]}`,
		`{"summary":"ok","related_entities":[{"type":"broadcast","id":"123","url":"/dashboard/item/123#nonce=secret"}]}`,
	}
	for _, raw := range invalid {
		if err := validateAttentionCommandResult(json.RawMessage(raw)); err == nil {
			t.Fatalf("invalid Attention result accepted: %s", raw)
		}
	}
}

func TestCanonicalizeAttentionCommandResultRemovesRuntimePresentation(t *testing.T) {
	raw := json.RawMessage(`{"summary":"完成","related_entities":[{"type":"broadcast","id":"123","label":"重新登录","url":"/dashboard/broadcasts/123"}]}`)
	canonical, err := canonicalizeAttentionCommandResult(raw)
	if err != nil {
		t.Fatal(err)
	}
	var result attentionCommandResult
	if err := json.Unmarshal(canonical, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.RelatedEntities) != 1 || result.RelatedEntities[0].URL != "/dashboard/broadcasts/123" ||
		result.RelatedEntities[0].Label != "" || result.RelatedEntities[0].TrustedPublic {
		t.Fatalf("canonical related entity=%+v", result.RelatedEntities)
	}
}

func TestAttentionCommandProtocolIsRequired(t *testing.T) {
	if !validAttentionCommandProtocol(`{"protocol_version":"agent_attention.v1"}`) {
		t.Fatal("valid Attention command protocol rejected")
	}
	for _, raw := range []string{`{}`, `{"protocol_version":"agent_attention.v2"}`, `null`} {
		if validAttentionCommandProtocol(raw) {
			t.Fatalf("unsupported Attention command protocol accepted: %s", raw)
		}
	}
}
