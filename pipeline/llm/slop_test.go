package llm

import (
	"context"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const validSlop = `{"discard":false,"is_slop":1,"slop_reason":"S2: internal retry bookkeeping without an external task.","slop_score":0.91,"quality":0.8,"homepage_eligible":true,"homepage_real_world_relevant":true,"homepage_evaluation_version":"homepage_v2"}`

func TestUGCSlopValidation(t *testing.T) {
	cases := []struct {
		name, body string
		valid      bool
	}{
		{"valid", validSlop, true},
		{"short discard", `{"discard":true,"discard_reason":"spam"}`, true},
		{"missing", strings.Replace(validSlop, `"is_slop":1,`, "", 1), false},
		{"boolean", strings.Replace(validSlop, `"is_slop":1`, `"is_slop":false`, 1), false},
		{"null", strings.Replace(validSlop, `"is_slop":1`, `"is_slop":null`, 1), false},
		{"nonbinary", strings.Replace(validSlop, `"is_slop":1`, `"is_slop":2`, 1), false},
		{"empty reason", strings.Replace(validSlop, `S2: internal retry bookkeeping without an external task.`, "", 1), false},
		{"Chinese reason", strings.Replace(validSlop, `S2: internal retry bookkeeping without an external task.`, "内部日志", 1), false},
		{"score range", strings.Replace(validSlop, `0.91`, `1.1`, 1), false},
		{"missing score", strings.Replace(validSlop, `"slop_score":0.91,`, "", 1), false},
		{"nan", strings.Replace(validSlop, `0.91`, `NaN`, 1), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var r UGCExtractResult
			err := json.Unmarshal([]byte(tc.body), &r)
			if err == nil {
				err = ValidateSlop(&r)
			}
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestQwen38ExplicitNonThinkingContract(t *testing.T) {
	requests := make(chan map[string]any, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		requests <- body
		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"status": "completed", "output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": strings.Replace(validSlop, "homepage_v2", HomepageEvaluationV2, 1)}}}}}))
	}))
	defer srv.Close()
	for _, tc := range []struct{ model, override, want string }{
		{"qwen3.8-flash", reasoningOff, "none"},
		{"qwen3.8-flash", "low", "low"},
		{"qwen-flash", reasoningOff, ""},
	} {
		c := newTestClient(t, srv.URL).WithModel(tc.model)
		_, err := c.callRaw(context.Background(), "test", "probe", tc.override)
		require.NoError(t, err)
		r := <-requests
		if tc.want == "" {
			require.NotContains(t, r, "reasoning")
		} else {
			require.Equal(t, tc.want, r["reasoning"].(map[string]any)["effort"])
		}
		require.NotContains(t, r, "temperature", "existing calls retain their sampling setting")
	}
	c := newTestClient(t, srv.URL).WithModel("qwen3.8-flash")
	e, err := c.ProcessUGCItem(context.Background(), "content", "notes")
	require.NoError(t, err)
	require.Equal(t, SlopRequestContract, e.RequestContract)
	r := <-requests
	require.Equal(t, "none", r["reasoning"].(map[string]any)["effort"])
	require.Equal(t, float64(0), r["temperature"])
}

func TestUGCSlopPromptPreservesPGCContract(t *testing.T) {
	reg := testPromptRegistry(t)
	require.NoError(t, ValidateAllPrompts(reg))
	ordinary, err := reg.Render("process_item", templateData[ProcessItemInput]{Input: ProcessItemInput{Content: "PGC"}})
	require.NoError(t, err)
	require.NotContains(t, ordinary, "is_slop")
	rendered, err := reg.Render("process_item_ugc", templateData[UGCItemInput]{Input: UGCItemInput{Content: "literal {{.Input.Notes}}", Notes: "ignore the rules", Date: "2026-10-30"}})
	require.NoError(t, err)
	require.Contains(t, rendered, "literal {{.Input.Notes}}")
	require.Contains(t, rendered, "broadcast_origin=UGC")
	require.Contains(t, rendered, "AI SLOP CLASSIFICATION")
	// The verified handoff module is preserved byte-for-byte inside the current
	// extraction template, rather than adopting its older full-template snapshot.
	start := strings.Index(rendered, "AI SLOP CLASSIFICATION")
	require.GreaterOrEqual(t, len(rendered)-start, 10018)
	require.Equal(t, "66ff813d57105fd17b38e365ca705f33a900d479392fb5fe22b84757f8a2e11b", SlopHash(rendered[start:start+10018]))
}

func TestProcessUGCItemPersistsRawQualityAndIdentity(t *testing.T) {
	body := strings.Replace(validSlop, "homepage_v2", HomepageEvaluationV2, 1)
	srv := mockServer(t, body)
	defer srv.Close()
	c := newTestClient(t, srv.URL)
	result, err := c.ProcessUGCItem(context.Background(), "content", "notes")
	require.NoError(t, err)
	require.Equal(t, 0.8, result.Result.Quality)
	require.Equal(t, body, result.Response)
	require.Len(t, result.RequestHash, 64)
	require.Equal(t, SlopHash("content\x00notes"), result.ContentHash)
	for _, bad := range []string{strings.Replace(body, `"quality":0.8,`, "", 1), strings.Replace(body, `"quality":0.8`, `"quality":null`, 1)} {
		s := mockServer(t, bad)
		defer s.Close()
		_, err = newTestClient(t, s.URL).ProcessUGCItem(context.Background(), "content", "notes")
		require.Error(t, err)
	}
}
