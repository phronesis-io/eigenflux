package api

import "testing"

func TestConsoleGlobalDailyPicksAreBoundedAndLocalized(t *testing.T) {
	now := int64(1787136000000)
	zh := consoleGlobalDailyPicks("zh", now)
	en := consoleGlobalDailyPicks("en", now)
	if len(zh) != 3 || len(en) != 3 {
		t.Fatalf("daily picks must contain exactly three items: zh=%d en=%d", len(zh), len(en))
	}
	for _, picks := range [][]map[string]interface{}{zh, en} {
		for _, pick := range picks {
			if pick["source"] != "EigenFlux" || pick["global_pick"] != true || pick["created_at"] != now {
				t.Fatalf("unexpected global pick envelope: %#v", pick)
			}
			if pick["content"] == "" || pick["summary"] == "" {
				t.Fatalf("global pick content must not be empty: %#v", pick)
			}
		}
	}
	if zh[0]["content"] == en[0]["content"] {
		t.Fatalf("daily picks should be localized: zh=%q en=%q", zh[0]["content"], en[0]["content"])
	}
}
