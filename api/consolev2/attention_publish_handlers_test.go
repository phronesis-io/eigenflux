package consolev2

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
)

func validAttentionBatch(now int64) attentionPublishRequest {
	return attentionPublishRequest{
		SchemaVersion: "agent_attention.v1", IdempotencyKey: "upload-01JY7K9M3Q4P8N2V6X5Z",
		Items: []attentionPublishItem{{
			ClientItemID: "item-01JY7K9M3Q4P8N2V6X5Z", Surface: "participation",
			Category: "action_recommendation", Language: "zh-CN", Title: "需要你的决定",
			Body: "Agent 已完成判断，需要用户选择后续动作。", Recommendation: "建议先观察。",
			SourceRef: &attentionSourceRef{Type: "broadcast", ID: "123"},
			Actions: []attentionProtocolAction{
				{ActionKey: "a1", Kind: "preset", Flag: "observe_first", Appearance: "primary"},
				{ActionKey: "a2", Kind: "custom", Flag: "继续研究", Appearance: "secondary"},
			}, GeneratedAt: now, ExpiresAt: now + int64(24*time.Hour/time.Millisecond),
		}},
	}
}

func TestValidateAttentionPublishAcceptsShortActionKeysAndUTF8CustomFlag(t *testing.T) {
	now := time.Now().UnixMilli()
	req := validAttentionBatch(now)
	if err := validateAttentionPublish(&req, now); err != nil {
		t.Fatalf("valid Attention batch rejected: %v", err)
	}
	req = validAttentionBatch(now)
	req.Items[0].Actions[1].Flag = "一二三四五六七"
	if err := validateAttentionPublish(&req, now); err == nil || !strings.Contains(err.Error(), "custom flag") {
		t.Fatalf("21-byte custom flag must be rejected, got %v", err)
	}
	req = validAttentionBatch(now)
	req.Items[0].Actions[1].Flag = "一二三四五六"
	if err := validateAttentionPublish(&req, now); err != nil {
		t.Fatalf("18-byte custom flag rejected: %v", err)
	}
}

func TestValidateAttentionPublishPreservesAgentAuthoredText(t *testing.T) {
	now := time.Now().UnixMilli()
	req := validAttentionBatch(now)
	req.Items[0].Title = "  Agent 的标题  "
	req.Items[0].Body = "\nAgent 的正文\n"
	req.Items[0].Recommendation = "  Agent 的建议  "
	if err := validateAttentionPublish(&req, now); err != nil {
		t.Fatalf("authored whitespace must be accepted without rewriting: %v", err)
	}
	if req.Items[0].Title != "  Agent 的标题  " || req.Items[0].Body != "\nAgent 的正文\n" ||
		req.Items[0].Recommendation != "  Agent 的建议  " {
		t.Fatalf("Agent-authored text was rewritten: %#v", req.Items[0])
	}

	req = validAttentionBatch(now)
	req.Items[0].Actions[1].Flag = " 继续研究"
	if err := validateAttentionPublish(&req, now); err == nil || !strings.Contains(err.Error(), "custom flag") {
		t.Fatalf("custom flag with surrounding whitespace must be rejected, got %v", err)
	}
}

func TestValidateAttentionPublishRejectsPartialOrUnsafeActions(t *testing.T) {
	now := time.Now().UnixMilli()
	tests := []struct {
		name   string
		mutate func(*attentionPublishRequest)
	}{
		{"no actions", func(req *attentionPublishRequest) { req.Items[0].Actions = nil }},
		{"two primary", func(req *attentionPublishRequest) { req.Items[0].Actions[1].Appearance = "primary" }},
		{"cross surface flag", func(req *attentionPublishRequest) { req.Items[0].Actions[0].Flag = "open_source" }},
		{"html custom", func(req *attentionPublishRequest) { req.Items[0].Actions[1].Flag = "<b>继续</b>" }},
		{"seconds timestamp", func(req *attentionPublishRequest) { req.Items[0].GeneratedAt = time.Now().Unix() }},
		{"invalid optional context", func(req *attentionPublishRequest) {
			req.Items[0].ContextRef = attentionContextRef{Operation: "unexpected"}
		}},
		{"open source without source", func(req *attentionPublishRequest) {
			req.Items[0].Surface = "focus"
			req.Items[0].Category = "other_attention"
			req.Items[0].Recommendation = ""
			req.Items[0].SourceRef = nil
			req.Items[0].Actions = []attentionProtocolAction{{ActionKey: "source", Kind: "preset", Flag: "open_source", Appearance: "primary"}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := validAttentionBatch(now)
			test.mutate(&req)
			if err := validateAttentionPublish(&req, now); err == nil {
				t.Fatal("invalid Attention batch was accepted")
			}
		})
	}
}

func TestAttentionRateLimiterIsAtomicAcrossBothSurfaces(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	service := &Service{redisClient: client}
	participation := make([]attentionPublishItem, 0, attentionHourlyParticipate)
	for index := 0; index < attentionHourlyParticipate; index++ {
		participation = append(participation, attentionPublishItem{ClientItemID: "participation-" + string(rune('a'+index)), Surface: "participation"})
	}
	focus := make([]attentionPublishItem, 0, attentionHourlyFocus)
	for index := 0; index < attentionHourlyFocus; index++ {
		focus = append(focus, attentionPublishItem{ClientItemID: "focus-" + string(rune('a'+index)), Surface: "focus"})
	}
	if retry, err := service.allowAttentionPublish(context.Background(), 42, participation); err != nil || retry != 0 {
		t.Fatalf("participation quota batch rejected: retry=%d err=%v", retry, err)
	}
	if retry, err := service.allowAttentionPublish(context.Background(), 42, focus[:10]); err != nil || retry != 0 {
		t.Fatalf("first focus batch rejected: retry=%d err=%v", retry, err)
	}
	if retry, err := service.allowAttentionPublish(context.Background(), 42, focus[10:]); err != nil || retry != 0 {
		t.Fatalf("second focus batch rejected: retry=%d err=%v", retry, err)
	}
	if retry, err := service.allowAttentionPublish(context.Background(), 42, participation); err != nil || retry != 0 {
		t.Fatalf("stable client IDs must replay without quota: retry=%d err=%v", retry, err)
	}
	if retry, err := service.allowAttentionPublish(context.Background(), 42,
		[]attentionPublishItem{{ClientItemID: "another-focus", Surface: "focus"}}); err == nil || retry <= 0 {
		t.Fatalf("21st distinct item was not rejected: retry=%d err=%v", retry, err)
	}
}

func TestAttentionRateReservationCanBeReleasedAfterPersistenceFailure(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	service := &Service{redisClient: client}
	items := []attentionPublishItem{
		{ClientItemID: "release-participation", Surface: "participation"},
		{ClientItemID: "release-focus", Surface: "focus"},
	}
	if retry, err := service.allowAttentionPublish(context.Background(), 43, items); err != nil || retry != 0 {
		t.Fatalf("reservation rejected: retry=%d err=%v", retry, err)
	}
	if err := service.releaseAttentionPublish(context.Background(), 43, items); err != nil {
		t.Fatalf("reservation release failed: %v", err)
	}
	for _, suffix := range []string{"total", "participation", "focus"} {
		count, err := client.ZCard(context.Background(), "console:v2:attention:{43}:"+suffix).Result()
		if err != nil {
			t.Fatalf("could not read released %s quota: %v", suffix, err)
		}
		if count != 0 {
			t.Fatalf("released %s quota still contains %d members", suffix, count)
		}
	}
}

func TestAttentionPresetDisplayTextMatchesConsoleLabels(t *testing.T) {
	tests := []struct {
		flag, language, expected string
	}{
		{"apply_intent_update", "zh-CN", "接受意图更新"},
		{"ask_agent_summarize", "zh-CN", "帮我总结影响"},
		{"draft_broadcast", "zh-CN", "把它发展成广播"},
		{"apply_goal_update", "en", "Update recent focus"},
	}
	for _, test := range tests {
		if actual := attentionActionDisplay(test.flag, test.language); actual != test.expected {
			t.Fatalf("display text for %s/%s = %q, want %q", test.flag, test.language, actual, test.expected)
		}
	}
}
