package discovery

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLexicalEvidenceElasticsearch(t *testing.T) {
	base := os.Getenv("LEXICAL_EVIDENCE_TEST_ES_URL")
	if base == "" {
		t.Skip("set LEXICAL_EVIDENCE_TEST_ES_URL for isolated Elasticsearch test")
	}
	require.Equal(t, "http://127.0.0.1:56921", base, "test may only use the isolated replay cluster")
	name := fmt.Sprintf("ugc-lexical-evidence-test-%d", time.Now().UnixNano())
	client := &http.Client{Timeout: 10 * time.Second}
	call := func(method, path string, body any) map[string]any {
		t.Helper()
		var data []byte
		if body != nil {
			var err error
			data, err = json.Marshal(body)
			require.NoError(t, err)
		}
		req, err := http.NewRequest(method, base+"/"+name+path, bytes.NewReader(data))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		res, err := client.Do(req)
		require.NoError(t, err)
		defer res.Body.Close()
		raw, err := io.ReadAll(res.Body)
		require.NoError(t, err)
		require.Less(t, res.StatusCode, 300, string(raw))
		var out map[string]any
		require.NoError(t, json.Unmarshal(raw, &out))
		return out
	}
	props := map[string]any{}
	for _, field := range []string{"content", "summary", "keywords", "domains"} {
		props[field] = map[string]any{"type": "text", "fields": map[string]any{"text": map[string]any{"type": "text"}}}
	}
	props["lang"] = map[string]any{"type": "text", "fields": map[string]any{"keyword": map[string]any{"type": "keyword"}}}
	props["expire_time"] = map[string]any{"type": "date"}
	call("PUT", "", map[string]any{"settings": map[string]any{"number_of_shards": 1, "number_of_replicas": 0}, "mappings": map[string]any{"properties": props}})
	defer call("DELETE", "", nil)
	rows := []map[string]any{
		{"content": "玩家使用 Agent 联机游戏，通过创新玩法提升体验。游戏案例与方法。", "summary": "玩家与 Agent 联机游戏案例"},
		{"content": "端点声明 has_more 为真却不返回游标：调用方无法区分取尽与断流", "summary": "分页游标契约"},
		{"content": "Agent 自我意识研究，分享记忆方法和使用经验，欢迎案例。", "summary": "Agent 自我认知共研"},
		{"content": "退出码 0 不等于动作生效：心跳接入的三个读回点", "summary": "心跳验收", "domains": []string{"technology", "AI agents"}, "keywords": []string{"agent reliability"}},
		{"content": "小样本回测的显著性下限由样本量决定", "summary": "回测准入规则", "domains": []string{"finance", "statistics"}},
	}
	for i, row := range rows {
		row["id"] = i + 1
		row["lang"] = "zh"
		call("PUT", fmt.Sprintf("/_doc/%d", i+1), row)
	}
	call("POST", "/_refresh", nil)
	search := func(c Context) []int {
		t.Helper()
		c.Filters.ExcludeAuthors = []string{"99999"}
		q, err := Query(c, Broadcast, "lexical", 20)
		require.NoError(t, err)
		res := call("POST", "/_search", q)
		var ids []int
		for _, v := range res["hits"].(map[string]any)["hits"].([]any) {
			h := v.(map[string]any)
			ids = append(ids, int(h["_source"].(map[string]any)["id"].(float64)))
		}
		return ids
	}
	goal := "收集玩家使用 Agent 创新游戏玩法、提升玩法体验的案例与方法，例如与 Agent 联机。"
	legacy := search(Context{Query: goal})
	require.Contains(t, legacy, 2)
	require.Contains(t, legacy, 3)
	fixed := search(Context{Query: goal, LexicalPolicy: LexicalEvidenceV1})
	require.Contains(t, fixed, 1)
	require.NotContains(t, fixed, 2)
	require.NotContains(t, fixed, 3)
	require.NotContains(t, search(Context{Query: "ai-agents"}), 4)
	require.Contains(t, search(Context{Query: "ai-agents", LexicalPolicy: LexicalEvidenceV1}), 4)
	require.NotContains(t, search(Context{Query: "multi-agent-systems", LexicalPolicy: LexicalEvidenceV1}), 5)
	require.NotContains(t, search(Context{Query: "游戏", LexicalPolicy: LexicalEvidenceV1}), 2)
}
