package consolev2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"time"

	"eigenflux_server/pipeline/llm"
	"eigenflux_server/pkg/config"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"strings"
	"testing"
)

type todayBriefRetryGenerator struct {
	generated  string
	compressed string
	compresses int
}

func (g *todayBriefRetryGenerator) Generate(context.Context, todayBriefFacts, string) (string, error) {
	return g.generated, nil
}

func (g *todayBriefRetryGenerator) Compress(_ context.Context, _ string, _ string, _ int) (string, error) {
	g.compresses++
	return g.compressed, nil
}

func TestTodayWorkingLanguagesPreferAgentCardOrder(t *testing.T) {
	languages := todayWorkingLanguages(
		`{"working_languages":["中文","English","zh-CN"]}`,
		`{"working_languages":["en"]}`,
	)
	if len(languages) != 2 || languages[0] != todayBriefChinese || languages[1] != todayBriefEnglish {
		t.Fatalf("languages=%#v", languages)
	}
	selected, err := selectTodayBriefLanguage("en", languages)
	if err != nil || selected != todayBriefEnglish {
		t.Fatalf("selected=%q err=%v", selected, err)
	}
	selected, err = selectTodayBriefLanguage("fr", languages)
	if err == nil || selected != "" {
		t.Fatalf("unsupported language selected=%q err=%v", selected, err)
	}
}

func TestTodayBriefLanguageFallsBackToPrimaryWorkingLanguage(t *testing.T) {
	selected, err := selectTodayBriefLanguage("en", []string{todayBriefChinese})
	if err != nil || selected != todayBriefChinese {
		t.Fatalf("selected=%q err=%v", selected, err)
	}
	selected, err = selectTodayBriefLanguage("en", nil)
	if err != nil || selected != todayBriefEnglish {
		t.Fatalf("selected=%q err=%v", selected, err)
	}
}

func TestTodayBriefHashIncludesLanguageAndFacts(t *testing.T) {
	facts := todayBriefFacts{Day: "2026-08-27", AgentName: "Atlas", FocusCount: 2}
	zh, err := todayBriefHash(facts, todayBriefChinese)
	if err != nil {
		t.Fatal(err)
	}
	en, err := todayBriefHash(facts, todayBriefEnglish)
	if err != nil {
		t.Fatal(err)
	}
	if zh == en || len(zh) != 64 || len(en) != 64 {
		t.Fatalf("unexpected hashes zh=%q en=%q", zh, en)
	}
	facts.FocusCount++
	changed, err := todayBriefHash(facts, todayBriefChinese)
	if err != nil {
		t.Fatal(err)
	}
	if changed == zh {
		t.Fatal("facts change did not invalidate the Today brief")
	}
}

func TestNormalizeTodayBriefTextProducesOneBoundedSentence(t *testing.T) {
	got, err := normalizeTodayBriefText("“今天，\nAtlas 发现了新的协作机会。”")
	if err != nil {
		t.Fatal(err)
	}
	if got != "今天， Atlas 发现了新的协作机会。" {
		t.Fatalf("got=%q", got)
	}
	tooLong := make([]rune, todayBriefMaxRunes+1)
	for index := range tooLong {
		tooLong[index] = '长'
	}
	if _, err := normalizeTodayBriefText(string(tooLong)); err == nil {
		t.Fatal("overlong Today brief was accepted")
	}
}

func TestTodayBriefLanguageLimitsCountUnicodeRunes(t *testing.T) {
	zhBoundary := strings.Repeat("界", todayBriefChineseMaxRunes)
	if _, err := normalizeTodayBriefTextForLanguage(zhBoundary, todayBriefChinese); err != nil {
		t.Fatalf("Chinese boundary rejected: %v", err)
	}
	if _, err := normalizeTodayBriefTextForLanguage(zhBoundary+"界", todayBriefChinese); !errors.Is(err, errTodayBriefTooLong) {
		t.Fatalf("Chinese overflow err=%v", err)
	}
	enBoundary := strings.Repeat("a", todayBriefEnglishMaxRunes)
	if _, err := normalizeTodayBriefTextForLanguage(enBoundary, todayBriefEnglish); err != nil {
		t.Fatalf("English boundary rejected: %v", err)
	}
	if _, err := normalizeTodayBriefTextForLanguage(enBoundary+"b", todayBriefEnglish); !errors.Is(err, errTodayBriefTooLong) {
		t.Fatalf("English overflow err=%v", err)
	}
}

func TestTodayBriefRetriesOverLimitGenerationOnce(t *testing.T) {
	generator := &todayBriefRetryGenerator{
		generated:  strings.Repeat("长", todayBriefChineseMaxRunes+1),
		compressed: "今日暂无需要介入的新事项。",
	}
	got, err := generateNormalizedTodayBrief(context.Background(), generator, todayBriefFacts{}, todayBriefChinese)
	if err != nil || got != generator.compressed {
		t.Fatalf("brief=%q err=%v", got, err)
	}
	if generator.compresses != 1 {
		t.Fatalf("compress attempts=%d", generator.compresses)
	}
	generator.compressed = strings.Repeat("仍", todayBriefChineseMaxRunes+1)
	if got, err := generateNormalizedTodayBrief(context.Background(), generator, todayBriefFacts{}, todayBriefChinese); err != nil || got != generator.compressed {
		t.Fatalf("wrapped sentence lost: %q %v", got, err)
	}
	generator.compressed = strings.Repeat("长", 91)
	if _, err := generateNormalizedTodayBrief(context.Background(), generator, todayBriefFacts{}, todayBriefChinese); !errors.Is(err, errTodayBriefTooLong) {
		t.Fatalf("second over-limit result err=%v", err)
	}
}

func TestTodayBriefPublicViewMarksChangedFactsStale(t *testing.T) {
	row := todayBriefRow{
		Language: todayBriefChinese, Day: "2026-08-27", FactsHash: "old",
		Narrative: "旧简报。", Status: "ready", GeneratedAt: 100, LastAttemptAt: 100,
	}
	view := todayBriefPublicView(row, "2026-08-27", "new", 200)
	if view["state"] != "ready" || view["stale"] != true || view["text"] != "旧简报。" {
		t.Fatalf("view=%#v", view)
	}
}

func TestTodayBriefPublicViewMapsPendingToGenerating(t *testing.T) {
	row := todayBriefRow{
		Language: todayBriefEnglish, Day: "2026-08-27", FactsHash: "same",
		Status: "pending", LeaseUntil: 10_000,
	}
	view := todayBriefPublicView(row, "2026-08-27", "same", 100)
	if view["state"] != "generating" || view["poll_after_ms"] != int64(2000) {
		t.Fatalf("view=%#v", view)
	}
	if got := mapTodayBriefWireState("ready"); got != "ready" {
		t.Fatalf("ready state changed to %q", got)
	}
}

// Capture the request at the model boundary without depending on live model output.
type todayBriefRecordingClient struct {
	prompts []string
	outputs []string
}

func (c *todayBriefRecordingClient) CallText(_ context.Context, prompt, _ string) (string, error) {
	c.prompts = append(c.prompts, prompt)
	output := c.outputs[0]
	c.outputs = c.outputs[1:]
	return output, nil
}

func TestTodayBriefNameInstructionsReachGenerationAndCompression(t *testing.T) {
	for _, language := range []string{todayBriefChinese, todayBriefEnglish} {
		for _, name := range []string{"Monster", "怪物", "Monster 小助手", "MøNster & Co."} {
			t.Run(language+"/"+name, func(t *testing.T) {
				want := name + " found a useful update."
				if language == todayBriefChinese {
					want = name + "发现了一条有用的信息。"
				}
				client := &todayBriefRecordingClient{outputs: []string{
					name + strings.Repeat("x", todayBriefLimit(language)), want,
				}}
				got, err := generateNormalizedTodayBrief(context.Background(), &llmTodayBriefGenerator{client: client}, todayBriefFacts{AgentName: name}, language)
				if err != nil || got != want {
					t.Fatalf("brief=%q err=%v", got, err)
				}
				if len(client.prompts) != 2 {
					t.Fatalf("expected generation and compression, got %d calls", len(client.prompts))
				}
				for _, prompt := range client.prompts {
					for _, rule := range []string{"Never translate, transliterate, localize", "append a role label", "Names in another language are allowed", "subject-free sentence or pronoun"} {
						if !strings.Contains(prompt, rule) {
							t.Errorf("model request missing name protection %q", rule)
						}
					}
					target := "Simplified Chinese"
					if language == todayBriefEnglish {
						target = "English"
					}
					if !strings.Contains(prompt, "sentence in "+target) {
						t.Errorf("model request missing target language %q", target)
					}
				}
				if !strings.Contains(client.prompts[0], "copy agent_name exactly") {
					t.Error("generation must explicitly preserve the display name")
				}
				if !strings.Contains(client.prompts[1], name) {
					t.Error("compression input lost the original name")
				}
			})
		}
	}
}

// Exercise the actual Service wiring and Responses HTTP payload, including the
// second model call when a headline exceeds the language limit.
func TestTodayBriefUsesQwen38WithoutThinkingForGenerationAndCompression(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		call := calls.Add(1)
		if r.URL.Path != "/v1/responses" {
			t.Errorf("path=%s", r.URL.Path)
		}
		wantModel, wantEffort := "qwen3.8-flash", "none"
		if call == 3 {
			wantModel, wantEffort = "qwen3.6-flash", "low"
		}
		if body["model"] != wantModel {
			t.Errorf("call %d model=%v", call, body["model"])
		}
		reasoning, _ := body["reasoning"].(map[string]interface{})
		if reasoning["effort"] != wantEffort {
			t.Errorf("call %d reasoning=%v", call, body["reasoning"])
		}
		if body["max_output_tokens"] != float64(4096) {
			t.Errorf("token limit=%v", body["max_output_tokens"])
		}
		text := "今天暂无新的待办事项。"
		if call == 1 {
			text = strings.Repeat("长", 61)
		}
		if call == 2 && !strings.Contains(body["input"].(string), "Compress the supplied Today headline") {
			t.Error("expected compression request")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"id": "resp-test", "object": "response", "status": "completed",
			"output": []interface{}{map[string]interface{}{"type": "message", "role": "assistant", "content": []interface{}{map[string]interface{}{"type": "output_text", "text": text}}}},
		})
	}))
	defer srv.Close()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	cfg := &config.Config{ConsoleV2PublicURL: "https://console.example.test", ConsoleV2OTPPepper: "test", LLMApiKey: "test", LLMBaseURL: srv.URL, LLMModel: "qwen3.6-flash", LLMReasoningEffort: "low", LLMMaxTokens: 4096}
	svc, err := NewService(db, &fixedIDGenerator{}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	text, err := generateNormalizedTodayBrief(ctx, svc.todayBriefGenerator, todayBriefFacts{Day: "2026-11-02", AgentName: "Atlas"}, todayBriefChinese)
	if err != nil || text != "今天暂无新的待办事项。" {
		t.Fatalf("text=%q err=%v", text, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("generation/compression calls=%d", calls.Load())
	}
	// The brief's override must not leak into other clients built from cfg.
	if _, err := llm.NewClient(cfg, nil).CallText(ctx, "global client", "test"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatalf("calls=%d", calls.Load())
	}
}
