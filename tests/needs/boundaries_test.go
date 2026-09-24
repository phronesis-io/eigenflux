package needs_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// Case IDs correspond to docs/design/need-capture/e2e.md.
func TestNeedInputHTTPBoundaries(t *testing.T) {
	h := setup(t)
	base := fmt.Sprintf(`{"schema_version":"need_input.v1","intent_id":"%d","intent_version":1,"need_type":"commission","target":{"desc":"indexes","candidate_needs":["indexes"]}}`, h.intent)
	field := func(s string) string { return strings.Replace(base, `"target":`, s+`,"target":`, 1) }
	desc := func(s string) string {
		b, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Replace(base, `"desc":"indexes"`, `"desc":`+string(b), 1)
	}
	phrases := func(n int) string {
		return strings.Replace(base, `["indexes"]`, `[`+strings.TrimSuffix(strings.Repeat(`"indexes",`, n), ",")+`]`, 1)
	}
	cases := []struct {
		name, body, key string
		status          int
	}{
		{"B01_ascii_200", desc(strings.Repeat("a", 200)), "", 201},
		{"B01_ascii_201", desc(strings.Repeat("a", 201)), "", 400},
		{"B01_cjk_100", desc(strings.Repeat("中", 100)), "", 201},
		{"B01_cjk_101", desc(strings.Repeat("中", 101)), "", 400},
		{"B01_blank", desc(" \t\n"), "", 400},
		{"B01_nul", desc("a\x00b"), "", 400},
		{"B02_zero_phrases", phrases(0), "", 400},
		{"B02_one_phrase", phrases(1), "", 201},
		{"B02_ten_phrases", phrases(10), "", 201},
		{"B02_eleven_phrases", phrases(11), "", 400},
		{"B03_priority_zero", field(`"priority":0`), "", 201},
		{"B03_priority_one", field(`"priority":1`), "", 201},
		{"B03_priority_negative", field(`"priority":-0.01`), "", 400},
		{"B03_priority_over", field(`"priority":1.01`), "", 400},
		{"B03_priority_null", field(`"priority":null`), "", 400},
		{"B04_zero_budget_delivery", field(`"constraints":{"budget_max_fen":0,"currency":"CNY","max_promised_delivery_ms":0}`), "", 201},
		{"B04_negative_budget", field(`"constraints":{"budget_max_fen":-1,"currency":"CNY"}`), "", 400},
		{"B04_fractional_budget", field(`"constraints":{"budget_max_fen":0.5,"currency":"CNY"}`), "", 400},
		{"B04_missing_currency", field(`"constraints":{"budget_max_fen":1}`), "", 400},
		{"B04_noncommission_budget", strings.Replace(field(`"constraints":{"budget_max_fen":0,"currency":"CNY"}`), `"commission"`, `"broadcast"`, 1), "", 400},
		{"B05_past_deadline", field(`"constraints":{"deadline_ms":1}`), "", 201},
		{"B05_zero_deadline", field(`"constraints":{"deadline_ms":0}`), "", 400},
		{"B06_exact_body_limit", base + strings.Repeat(" ", 32768-len(base)), "", 201},
		{"B06_over_body_limit", base + strings.Repeat(" ", 32769-len(base)), "", 413},
		{"B07_key_7", base, "1234567", 400},
		{"B07_key_8", base, "12345678", 201},
		{"B07_key_128", base, strings.Repeat("k", 128), 201},
		{"B07_key_129", base, strings.Repeat("k", 129), 400},
		{"B07_key_internal_space", base, "key with space", 400},
		{"B08_numeric_id", strings.Replace(base, fmt.Sprintf(`"%d"`, h.intent), fmt.Sprint(h.intent), 1), "", 400},
		{"B08_leading_zero_id", strings.Replace(base, fmt.Sprintf(`"%d"`, h.intent), fmt.Sprintf(`"0%d"`, h.intent), 1), "", 400},
		{"B08_overflow_id", strings.Replace(base, fmt.Sprint(h.intent), "9223372036854775808", 1), "", 400},
		{"B09_duplicate_field", field(`"need_type":"commission"`), "", 400},
		{"B09_derived_field", field(`"normalized_need":{}`), "", 400},
		{"B09_trailing_json", base + `{}`, "", 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key := tc.key
			if key == "" {
				key = "boundary-" + tc.name
			}
			got := h.request(t, "POST", "/need-inputs", h.token, key, tc.body, tc.status)
			var inputs, projections int64
			if err := h.db.Raw("SELECT count(*) FROM need_inputs WHERE agent_id=? AND idempotency_key=?", h.owner, key).Scan(&inputs).Error; err != nil {
				t.Fatal(err)
			}
			if err := h.db.Raw("SELECT count(*) FROM normalized_needs n JOIN need_inputs i USING (need_input_id) WHERE i.agent_id=? AND i.idempotency_key=?", h.owner, key).Scan(&projections).Error; err != nil {
				t.Fatal(err)
			}
			if tc.status == 201 {
				r := got["need_input"].(map[string]any)
				if inputs != 1 || projections != 1 || r["status"] != "normalized" || r["normalized_need"].(map[string]any)["eligible"] != true {
					t.Fatalf("not atomically available: %v, rows=%d/%d", got, inputs, projections)
				}
			} else {
				code := "INVALID_NEED_INPUT"
				if tc.status == 413 {
					code = "NEED_INPUT_TOO_LARGE"
				}
				if got["error"].(map[string]any)["code"] != code || inputs != 0 || projections != 0 {
					t.Fatalf("incorrect rejection: %v, rows=%d/%d", got, inputs, projections)
				}
			}
		})
	}
}

func TestNeedInputHTTPRevisionLifecycle(t *testing.T) {
	h := setup(t)
	body := strings.Replace(original, "INTENT_ID", fmt.Sprint(h.intent), 1)
	created := h.request(t, "POST", "/need-inputs", h.token, "lifecycle-v1", body, 201)["need_input"].(map[string]any)
	id := created["need_input_id"].(string)
	// JSON object formatting changes do not alter retry identity.
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, []byte(body), "", "  "); err != nil {
		t.Fatal(err)
	}
	replay := h.request(t, "POST", "/need-inputs", h.token, "lifecycle-v1", pretty.String(), 200)
	if replay["replayed"] != true || replay["need_input"].(map[string]any)["need_input_id"] != id {
		t.Fatal(replay)
	}
	// Array order remains meaningful even when the normalized phrases are the same.
	reordered := strings.Replace(body, `["PostgreSQL 索引","数据库性能"]`, `["数据库性能","PostgreSQL 索引"]`, 1)
	h.request(t, "POST", "/need-inputs", h.token, "lifecycle-v1", reordered, 409)
	h.request(t, "PUT", "/agent-context/intent-actions/"+fmt.Sprint(h.intent), h.token, "", `{"expected_context_revision":1,"idempotency_key":"lifecycle-edit","watch_for":"Updated PostgreSQL resources","trigger_when":"useful resources","action_instruction":"summarize","action_policy":"analyze_only","priority":10}`, 200)
	sources := h.request(t, "GET", "/agent-context/intent-actions", h.token, "", "", 200)
	if sources["intent_actions"].([]any)[0].(map[string]any)["version"] != float64(2) {
		t.Fatal(sources)
	}
	stale := h.request(t, "POST", "/need-inputs", h.token, "lifecycle-stale", body, 409)
	if stale["error"].(map[string]any)["code"] != "INTENT_REVISION_STALE" {
		t.Fatal(stale)
	}
	replay = h.request(t, "POST", "/need-inputs", h.token, "lifecycle-v1", body, 200)
	old := replay["need_input"].(map[string]any)
	if replay["replayed"] != true || old["need_input_id"] != id || old["normalized_need"].(map[string]any)["eligible"] != false || !reflect.DeepEqual(old["input"], created["input"]) || !reflect.DeepEqual(old["intent_snapshot"], created["intent_snapshot"]) {
		t.Fatal("retry changed history or eligibility", replay)
	}
	v2 := strings.Replace(body, `"intent_version":1`, `"intent_version":2`, 1)
	fresh := h.request(t, "POST", "/need-inputs", h.token, "lifecycle-v2", v2, 201)["need_input"].(map[string]any)
	if fresh["need_input_id"] == id || fresh["normalized_need"].(map[string]any)["eligible"] != true {
		t.Fatal(fresh)
	}
	page := h.request(t, "GET", "/need-inputs?limit=100", h.token, "", "", 200)
	rows := page["need_inputs"].([]any)
	if len(rows) != 2 || rows[0].(map[string]any)["need_input_id"] != fresh["need_input_id"] || rows[1].(map[string]any)["normalized_need"].(map[string]any)["eligible"] != false {
		t.Fatal(page)
	}
	foreign := h.request(t, "GET", "/need-inputs", h.otherToken, "", "", 200)
	if len(foreign["need_inputs"].([]any)) != 0 {
		t.Fatal("foreign inputs leaked", foreign)
	}
	var eligible int64
	if err := h.db.Raw("SELECT count(*) FROM current_normalized_needs WHERE agent_id=? AND intent_version=2", h.owner).Scan(&eligible).Error; err != nil || eligible != 1 {
		t.Fatal("current view disagrees with API", eligible, err)
	}
}
