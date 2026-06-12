package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"testing"
	"time"

	"eigenflux_server/pkg/replaylog"
	"eigenflux_server/tests/testutil"
)

// TestSortContextFeaturesInReplayLog verifies the full propagation path of
// the new client-context headers (introduced in this PR cluster):
//
//	HTTP X-Client-*  →  ClientInfoMiddleware
//	→ Kitex metainfo (pkg/reqinfo)
//	→ sort.SortItems handler
//	→ buildContextFeatures projection
//	→ agent_features JSON
//	→ feed.publishReplayLog
//	→ Redis stream:replay:log
//
// The test fetches the feed via HTTP with the documented header set, then
// reads the tail of the replay-log stream and asserts that the entry's
// agent_features JSON carries the nested "context" block with every key
// the gateway middleware extracts. This is end-to-end across api/, rpc/sort/,
// rpc/feed/, and pkg/{reqinfo,replaylog} — exactly the surface the headers
// touch in production.
func TestSortContextFeaturesInReplayLog(t *testing.T) {
	testutil.WaitForAPI(t)
	testutil.CleanTestData(t)

	// --- Seed: register one author and one user, publish items the user's profile matches.
	timestamp := time.Now().UnixNano()
	authorEmail := fmt.Sprintf("sort_ctx_author_%d@test.com", timestamp)
	userEmail := fmt.Sprintf("sort_ctx_user_%d@test.com", timestamp)
	t.Cleanup(func() { testutil.CleanupTestEmails(t, authorEmail, userEmail) })

	authorReg := testutil.RegisterAgent(t, authorEmail, "CtxAuthor", "Writes about AI and ML")
	userReg := testutil.RegisterAgent(t, userEmail, "CtxUser", "")
	authorToken := authorReg["token"].(string)
	userToken := userReg["token"].(string)
	userID := testutil.MustID(t, userReg["agent_id"], "agent_id")

	testutil.UpdateProfile(t, userToken,
		"Interested in artificial intelligence, machine learning, large language models")
	testutil.WaitForProfileProcessed(t, userID)

	// Use the longer-form study content known to survive the LLM filter
	// (same shape as items in TestE2EFullFlow / TestGetItemErrorCases).
	publishResp := testutil.PublishItem(t, authorToken,
		"Microsoft Research released a comprehensive study on retrieval-augmented generation (RAG) techniques for enterprise knowledge management. The paper evaluates 12 different chunking strategies across 5 embedding models and finds that semantic chunking with overlap produces 35% better recall than fixed-size approaches. The study also introduces a novel hybrid retrieval pipeline combining dense and sparse retrievers that achieves state-of-the-art performance on domain-specific QA benchmarks.",
		"RAG techniques study with practical enterprise recommendations",
		"")
	itemID := testutil.MustID(t, publishResp["item_id"], "item_id")
	testutil.WaitForItemsProcessed(t, []int64{itemID})
	testutil.RefreshES(t)

	// --- Capture the current stream tail so we only inspect entries our request creates.
	rdb := testutil.GetRedisClient(t)
	ctx := context.Background()
	tailIDs, err := rdb.XRevRangeN(ctx, replaylog.StreamName, "+", "-", 1).Result()
	if err != nil {
		t.Fatalf("XRevRangeN failed: %v", err)
	}
	lastID := "0"
	if len(tailIDs) > 0 {
		lastID = tailIDs[0].ID
	}

	// --- Call /api/v1/feed with the full set of client headers documented in the PR.
	const (
		wantHost     = "openclaw/0.0.12"
		wantChannel  = "openclaw"
		wantClientID = "ab12cd34"
		wantOS       = "darwin/arm64"
		wantTZ       = "Asia/Shanghai"
		wantLang     = "zh-CN"
		wantCLIVer   = "0.0.7"
	)
	req, _ := http.NewRequest("GET",
		testutil.BaseURL+"/api/v1/items/feed?action=refresh&limit=5", nil)
	req.Header.Set("Authorization", "Bearer "+userToken)
	req.Header.Set("X-Client-Host", wantHost)
	req.Header.Set("X-Client-Channel", wantChannel)
	req.Header.Set("X-Client-ID", wantClientID)
	req.Header.Set("X-Client-OS", wantOS)
	req.Header.Set("X-Client-TZ", wantTZ)
	req.Header.Set("X-Client-Lang", wantLang)
	req.Header.Set("X-CLI-Ver", wantCLIVer)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("feed request failed: %v", err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("feed HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	// --- Poll the replay-log stream for a new entry matching userID. Feed
	// publishes the replay log in a goroutine (`go func() { ... }`), so we
	// give it a short retry window rather than reading once and bailing.
	var match map[string]interface{}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		entries, rangeErr := rdb.XRange(ctx, replaylog.StreamName, "("+lastID, "+").Result()
		if rangeErr != nil {
			t.Fatalf("XRange after %s failed: %v", lastID, rangeErr)
		}
		for _, e := range entries {
			agentIDStr, _ := e.Values["agent_id"].(string)
			if id, _ := strconv.ParseInt(agentIDStr, 10, 64); id == userID {
				if agentFeaturesStr, ok := e.Values["agent_features"].(string); ok && agentFeaturesStr != "" {
					var feat map[string]interface{}
					if err := json.Unmarshal([]byte(agentFeaturesStr), &feat); err == nil {
						match = feat
						break
					}
				}
			}
		}
		if match != nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if match == nil {
		t.Fatal("no replay-log entry observed for the test agent within 10s — confirm the feed handler is publishing the replay stream")
	}

	// --- Verify the context block.
	ctxFeat, ok := match["context"].(map[string]interface{})
	if !ok {
		t.Fatalf("agent_features.context missing or wrong type: %#v", match)
	}

	checks := []struct {
		key, want string
	}{
		{"client_host", wantHost},
		{"client_channel", wantChannel},
		{"client_id", wantClientID},
		{"client_os", wantOS},
		{"client_tz", wantTZ},
		{"client_lang", wantLang},
		{"cli_ver", wantCLIVer},
	}
	for _, c := range checks {
		if got, _ := ctxFeat[c.key].(string); got != c.want {
			t.Errorf("context.%s = %q, want %q", c.key, got, c.want)
		}
	}
	// cli_ver_num parses "0.0.7" to 7 via parseVersionNum.
	if got, _ := ctxFeat["cli_ver_num"].(float64); got != 7 {
		t.Errorf("context.cli_ver_num = %v, want 7", ctxFeat["cli_ver_num"])
	}
}
