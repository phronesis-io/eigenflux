package consolev2

import (
	"testing"
)

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
