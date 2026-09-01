package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"cli.eigenflux.ai/internal/client"
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

func validAttentionPrefillRequest() attentionPublishRequest {
	request := validAttentionPublishRequest()
	request.IdempotencyKey = "prefill-01JY7K9M3Q4P8N2V6X5Z"
	request.Items[0] = attentionItem{
		ClientItemID: "prefill-item-01JY7K9M3Q4P8N2V6X5Z", Surface: "focus",
		Category: "important_signal", Language: "zh-CN", Title: "值得关注的信号",
		Body:      "Agent 从 onboarding baseline Feed 中完成了只读判断。",
		SourceRef: &attentionSourceRef{Type: "broadcast", ID: "123"},
		Actions: []attentionAction{
			{ActionKey: "open", Kind: "preset", Flag: "open_source", Appearance: "primary"},
			{ActionKey: "skip", Kind: "preset", Flag: "not_interested", Appearance: "secondary"},
		},
		GeneratedAt: 1_787_600_000_000, ExpiresAt: 1_787_686_400_000,
	}
	return request
}

func TestValidateAttentionPrefillRequestAllowsOnlyReadOnlyBaselineFocus(t *testing.T) {
	request := validAttentionPrefillRequest()
	if err := validateAttentionPublishRequest(request); err != nil {
		t.Fatalf("valid Attention batch rejected: %v", err)
	}
	if err := validateAttentionPrefillRequest(request); err != nil {
		t.Fatalf("valid Attention Prefill rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*attentionPublishRequest)
	}{
		{"participation", func(request *attentionPublishRequest) { request.Items[0] = validAttentionPublishRequest().Items[0] }},
		{"non baseline source", func(request *attentionPublishRequest) { request.Items[0].SourceRef.Type = "private_message" }},
		{"context bound", func(request *attentionPublishRequest) {
			request.Items[0].ContextRef = &attentionContextRef{ContextRevision: 1}
		}},
		{"external action", func(request *attentionPublishRequest) { request.Items[0].Actions[0].Flag = "ask_agent_contact" }},
		{"custom action", func(request *attentionPublishRequest) {
			request.Items[0].Actions[0] = attentionAction{ActionKey: "custom", Kind: "custom", Flag: "查看", Appearance: "primary"}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := validAttentionPrefillRequest()
			test.mutate(&request)
			if err := validateAttentionPrefillRequest(request); err == nil {
				t.Fatal("unsafe Attention Prefill was accepted")
			}
		})
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

func TestValidateAttentionPublishRequestEnforcesProtocolBounds(t *testing.T) {
	now := int64(1_787_600_000_000)
	tests := []struct {
		name   string
		mutate func(*attentionPublishRequest)
		want   string
	}{
		{name: "empty batch", mutate: func(request *attentionPublishRequest) { request.Items = nil }, want: "items must contain between 1 and 10"},
		{name: "eleven items", mutate: func(request *attentionPublishRequest) {
			base := request.Items[0]
			request.Items = make([]attentionItem, 11)
			for index := range request.Items {
				request.Items[index] = base
				request.Items[index].ClientItemID = fmt.Sprintf("item-%08d", index)
			}
		}, want: "items must contain between 1 and 10"},
		{name: "no actions", mutate: func(request *attentionPublishRequest) { request.Items[0].Actions = nil }, want: "actions must contain between 1 and 5"},
		{name: "six actions", mutate: func(request *attentionPublishRequest) {
			request.Items[0].Actions = []attentionAction{
				{ActionKey: "a1", Kind: "preset", Flag: "observe_first", Appearance: "primary"},
				{ActionKey: "a2", Kind: "preset", Flag: "follow_up", Appearance: "secondary"},
				{ActionKey: "a3", Kind: "preset", Flag: "follow_up", Appearance: "secondary"},
				{ActionKey: "a4", Kind: "preset", Flag: "follow_up", Appearance: "secondary"},
				{ActionKey: "a5", Kind: "preset", Flag: "follow_up", Appearance: "secondary"},
				{ActionKey: "a6", Kind: "preset", Flag: "follow_up", Appearance: "secondary"},
			}
		}, want: "actions must contain between 1 and 5"},
		{name: "invalid idempotency key", mutate: func(request *attentionPublishRequest) { request.IdempotencyKey = "short" }, want: "idempotency_key"},
		{name: "invalid language", mutate: func(request *attentionPublishRequest) { request.Items[0].Language = "zh" }, want: "language must be zh-CN or en"},
		{name: "cross-surface category", mutate: func(request *attentionPublishRequest) { request.Items[0].Category = "important_signal" }, want: "not valid for surface"},
		{name: "required source missing", mutate: func(request *attentionPublishRequest) { request.Items[0].SourceRef = nil }, want: "source_ref is required"},
		{name: "future generated at", mutate: func(request *attentionPublishRequest) {
			request.Items[0].GeneratedAt = now + attentionFutureSkewMS + 1
			request.Items[0].ExpiresAt = request.Items[0].GeneratedAt + 1
		}, want: "cannot exceed 5 minutes in the future"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := validAttentionPublishRequest()
			test.mutate(&request)
			if err := validateAttentionPublishRequestAt(request, now); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("wanted %q validation error, got %v", test.want, err)
			}
		})
	}
}

func TestAttentionProtocolEnumsRemainExact(t *testing.T) {
	wantCategories := map[string][]string{
		"participation": {"action_recommendation", "goal_calibration", "intent_update", "other_decision"},
		"focus":         {"important_signal", "opportunity", "relationship_created", "relationship_feedback", "watch_update", "other_attention"},
	}
	wantFlags := map[string][]string{
		"participation": {"approve_first_contact", "observe_first", "apply_goal_update", "keep_goal", "apply_intent_update", "keep_intent", "follow_up", "not_interested"},
		"focus":         {"open_source", "ask_agent_contact", "add_watch", "ask_agent_summarize", "draft_broadcast", "follow_up", "not_interested"},
	}
	for surface, expected := range wantCategories {
		if len(attentionCategories[surface]) != len(expected) {
			t.Fatalf("%s category count = %d, want %d", surface, len(attentionCategories[surface]), len(expected))
		}
		for _, value := range expected {
			if _, ok := attentionCategories[surface][value]; !ok {
				t.Errorf("%s is missing category %q", surface, value)
			}
		}
	}
	for surface, expected := range wantFlags {
		if len(attentionPresetFlags[surface]) != len(expected) {
			t.Fatalf("%s preset count = %d, want %d", surface, len(attentionPresetFlags[surface]), len(expected))
		}
		for _, value := range expected {
			if _, ok := attentionPresetFlags[surface][value]; !ok {
				t.Errorf("%s is missing preset flag %q", surface, value)
			}
		}
	}
	wantSources := []string{"broadcast", "broadcast_reply", "friend_request", "relation", "private_message", "context", "activity"}
	if len(attentionSourceTypes) != len(wantSources) {
		t.Fatalf("source type count = %d, want %d", len(attentionSourceTypes), len(wantSources))
	}
	for _, value := range wantSources {
		if _, ok := attentionSourceTypes[value]; !ok {
			t.Errorf("source types are missing %q", value)
		}
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
	request := validAttentionPublishRequest()
	request.Items[0].Actions[1].Flag = strings.Repeat("a", attentionCustomFlagLimit)
	if err := validateAttentionPublishRequest(request); err != nil {
		t.Fatalf("custom flag at the 20-byte boundary was rejected: %v", err)
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

func TestRejectIntentAddRequiresMatchingLocalContext(t *testing.T) {
	config.SetHomeDir(t.TempDir())
	t.Cleanup(func() { config.SetHomeDir("") })
	request := validAttentionPublishRequest()
	request.Items[0].Category = "intent_update"
	request.Items[0].ContextRef = &attentionContextRef{ContextRevision: 7, Operation: "add"}
	request.Items[0].Actions = []attentionAction{{ActionKey: "accept", Kind: "preset", Flag: "apply_intent_update", Appearance: "primary"}}

	if err := rejectFullLocalIntentAdds("test", "agent-1", request); err == nil || !strings.Contains(err.Error(), "context pull") {
		t.Fatalf("missing cache must fail closed, got %v", err)
	}
	if err := controlcontext.Save("test", controlcontext.Snapshot{
		OwnerAgentID: "agent-1", Revision: 7, Context: json.RawMessage(`{"network_goal":{}}`),
	}); err != nil {
		t.Fatal(err)
	}
	if err := rejectFullLocalIntentAdds("test", "agent-1", request); err == nil || !strings.Contains(err.Error(), "context pull") {
		t.Fatalf("cache without intent_actions must fail closed, got %v", err)
	}
	if err := controlcontext.Save("test", controlcontext.Snapshot{
		OwnerAgentID: "agent-1", Revision: 8, Context: json.RawMessage(`{"intent_actions":[]}`),
	}); err != nil {
		t.Fatal(err)
	}
	if err := rejectFullLocalIntentAdds("test", "agent-1", request); err == nil || !strings.Contains(err.Error(), "latest locally applied context revision") {
		t.Fatalf("revision mismatch must fail closed, got %v", err)
	}
	if err := controlcontext.Save("test", controlcontext.Snapshot{
		OwnerAgentID: "agent-1", Revision: 7, Context: json.RawMessage(`{"intent_actions":[]}`),
	}); err != nil {
		t.Fatal(err)
	}
	if err := rejectFullLocalIntentAdds("test", "agent-1", request); err != nil {
		t.Fatalf("matching context below capacity must permit intent add: %v", err)
	}
}

func TestAttentionPublishCommandContract(t *testing.T) {
	if attentionPublishEndpoint != "/agent-attention-items:publish" {
		t.Fatalf("unexpected V2 publish path: %s", attentionPublishEndpoint)
	}
	if attentionPublishCmd.Flags().Lookup("stdin") == nil {
		t.Fatal("attention publish must expose --stdin")
	}
	if attentionPrefillEndpoint != "/agent-attention-items/prefill" {
		t.Fatalf("unexpected V2 prefill path: %s", attentionPrefillEndpoint)
	}
	if attentionPrefillCmd.Flags().Lookup("stdin") == nil {
		t.Fatal("attention prefill must expose --stdin")
	}
}

func TestAttentionPublishFormatsMachineReadableRetry(t *testing.T) {
	cause := &client.APIError{
		StatusCode:        http.StatusTooManyRequests,
		ErrorCode:         "ATTENTION_RATE_LIMITED",
		Msg:               "limit reached",
		RetryAfterSeconds: 37,
		Details:           json.RawMessage(`{"remaining":{"total":0},"retry_after_seconds":37}`),
	}
	err := formatAttentionPublishError(cause, "json")
	if !errors.Is(err, cause) {
		t.Fatal("machine-readable error must preserve the API error cause")
	}
	var payload struct {
		Status            int   `json:"status"`
		RetryAfterSeconds int64 `json:"retry_after_seconds"`
		Error             struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(err.Error()), &payload) != nil || payload.Status != http.StatusTooManyRequests ||
		payload.RetryAfterSeconds != 37 || payload.Error.Code != "ATTENTION_RATE_LIMITED" {
		t.Fatalf("unexpected machine-readable error: %s", err)
	}
	if got := formatAttentionPublishError(cause, "table"); got != cause {
		t.Fatal("human-readable format must preserve the original error")
	}
}

func stringPointer(value string) *string { return &value }
