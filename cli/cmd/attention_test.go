package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/controlcontext"
)

func validAttentionPublishRequest() attentionPublishRequest {
	return attentionPublishRequest{
		SchemaVersion:  attentionSchemaVersion,
		IdempotencyKey: "upload-01JY7K9M3Q4P8N2V6X5Z",
		Items: []attentionItem{{
			ClientItemID:   "item-01JY7K9M3Q4P8N2V6X5Z",
			Surface:        "participation",
			Category:       "action_recommendation",
			Language:       "zh-CN",
			Title:          "需要你的决定",
			Body:           "Agent 已完成判断，需要用户选择后续动作。",
			Recommendation: "建议先观察后再继续处理。",
			SourceRef:      &attentionSourceRef{Type: "broadcast", ID: "123"},
			Actions: []attentionAction{
				{ActionKey: "a1", Kind: "preset", Flag: "observe_first", Appearance: "primary"},
				{ActionKey: "a2", Kind: "custom", Flag: "继续研究", Appearance: "secondary"},
			},
			GeneratedAt: 1_787_600_000_000,
			ExpiresAt:   1_787_686_400_000,
		}},
	}
}

func TestValidateAttentionPublishRequestAcceptsTypedBatch(t *testing.T) {
	request := validAttentionPublishRequest()
	if err := validateAttentionPublishRequest(request); err != nil {
		t.Fatalf("valid attention batch rejected: %v", err)
	}
	request.Items[0] = attentionItem{
		ClientItemID: "focus-item", Surface: "focus", Category: "opportunity", Language: "en",
		Title: "Opportunity found", Body: "The Agent found a useful public signal.",
		SourceRef:   &attentionSourceRef{Type: "broadcast_reply", ID: "124", ParentID: stringPointer("123")},
		Actions:     []attentionAction{{ActionKey: "open", Kind: "preset", Flag: "open_source", Appearance: "secondary"}},
		GeneratedAt: 1_787_600_000_000, ExpiresAt: 1_787_686_400_000,
	}
	if err := validateAttentionPublishRequest(request); err != nil {
		t.Fatalf("valid focus attention batch rejected: %v", err)
	}
}

func TestReadAttentionPublishRequestRejectsUnknownAndOversizedJSON(t *testing.T) {
	request := validAttentionPublishRequest()
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	withUnknown := strings.Replace(string(encoded), `"schema_version"`, `"unknown":true,"schema_version"`, 1)
	if _, err := readAttentionPublishRequest(strings.NewReader(withUnknown)); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown JSON field must be rejected, got %v", err)
	}
	if _, err := readAttentionPublishRequest(strings.NewReader(strings.Repeat("x", attentionBodyLimit+1))); err == nil || !strings.Contains(err.Error(), "32 KiB") {
		t.Fatalf("oversized payload must be rejected, got %v", err)
	}
}

func TestValidateAttentionPublishRequestEnforcesActionContract(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*attentionPublishRequest)
		want   string
	}{
		{
			name: "cross-surface preset",
			mutate: func(request *attentionPublishRequest) {
				request.Items[0].Actions[0].Flag = "open_source"
			},
			want: "not valid for surface",
		},
		{
			name: "two primary actions",
			mutate: func(request *attentionPublishRequest) {
				request.Items[0].Actions[1].Appearance = "primary"
			},
			want: "at most one primary",
		},
		{
			name: "custom flag exceeds utf8 byte limit",
			mutate: func(request *attentionPublishRequest) {
				request.Items[0].Actions[1].Flag = "这是超过二十字节的操作"
			},
			want: "1-20 UTF-8 bytes",
		},
		{
			name: "custom flag contains html",
			mutate: func(request *attentionPublishRequest) {
				request.Items[0].Actions[1].Flag = "<b>继续</b>"
			},
			want: "newlines or HTML",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := validAttentionPublishRequest()
			test.mutate(&request)
			if err := validateAttentionPublishRequest(request); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("wanted %q validation error, got %v", test.want, err)
			}
		})
	}
}

func TestValidateAttentionPublishRequestEnforcesCalibrationContext(t *testing.T) {
	request := validAttentionPublishRequest()
	item := &request.Items[0]
	item.Category = "intent_update"
	item.ContextRef = &attentionContextRef{ContextRevision: 9, Operation: "add"}
	item.Actions = []attentionAction{{ActionKey: "accept", Kind: "preset", Flag: "apply_intent_update", Appearance: "primary"}}
	if err := validateAttentionPublishRequest(request); err != nil {
		t.Fatalf("valid intent add rejected: %v", err)
	}
	item.ContextRef.Operation = ""
	if err := validateAttentionPublishRequest(request); err == nil || !strings.Contains(err.Error(), "requires context_ref.operation") {
		t.Fatalf("intent operation must be required, got %v", err)
	}
	item.ContextRef = &attentionContextRef{ContextRevision: 9, Operation: "update"}
	if err := validateAttentionPublishRequest(request); err == nil || !strings.Contains(err.Error(), "positive intent_id") {
		t.Fatalf("intent update must bind an intent, got %v", err)
	}

	item.Category = "goal_calibration"
	item.ContextRef = &attentionContextRef{ContextRevision: 9, NetworkGoalRevision: 3}
	item.Actions[0].Flag = "apply_goal_update"
	if err := validateAttentionPublishRequest(request); err != nil {
		t.Fatalf("valid goal calibration rejected: %v", err)
	}
}

func TestRejectFullLocalIntentAdds(t *testing.T) {
	config.SetHomeDir(t.TempDir())
	t.Cleanup(func() { config.SetHomeDir("") })
	intents := make([]map[string]string, 10)
	for index := range intents {
		intents[index] = map[string]string{"status": "active"}
	}
	contextBody, _ := json.Marshal(map[string]interface{}{"context_revision": 7, "intent_actions": intents})
	if err := controlcontext.Save("test", controlcontext.Snapshot{
		OwnerAgentID: "agent-1", Revision: 7, Context: contextBody,
	}); err != nil {
		t.Fatal(err)
	}
	request := validAttentionPublishRequest()
	request.Items[0].Category = "intent_update"
	request.Items[0].ContextRef = &attentionContextRef{ContextRevision: 7, Operation: "add"}
	request.Items[0].Actions = []attentionAction{{ActionKey: "accept", Kind: "preset", Flag: "apply_intent_update", Appearance: "primary"}}
	if err := rejectFullLocalIntentAdds("test", "agent-1", request); err == nil || !strings.Contains(err.Error(), "active intent limit reached") {
		t.Fatalf("local active-intent limit must reject add suggestion, got %v", err)

	}
	request.Items[0].ContextRef.Operation = "update"
	request.Items[0].ContextRef.IntentID = stringPointer("11")
	if err := rejectFullLocalIntentAdds("test", "agent-1", request); err != nil {
		t.Fatalf("updating an existing intent must remain allowed: %v", err)
	}
}

func TestAttentionPublishCommandContract(t *testing.T) {
	if attentionPublishEndpoint != "/agent-attention-items:publish" {
		t.Fatalf("unexpected V2 publish path: %s", attentionPublishEndpoint)
	}
	if attentionPublishCmd.Flags().Lookup("stdin") == nil {
		t.Fatal("attention publish must expose --stdin")
	}
}

func stringPointer(value string) *string { return &value }
