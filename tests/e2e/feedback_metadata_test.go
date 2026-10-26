package e2e_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	apidal "eigenflux_server/api/dal"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/itemstats"
	"eigenflux_server/pkg/recommendationmetrics"
	"eigenflux_server/tests/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// Feedback persistence does not depend on LLM enrichment. Seed completed items
// and exercise the real authenticated API, stream worker, database and counters.
func feedbackMetadataFixture(t *testing.T) (string, int64, int64, int64) {
	t.Helper()
	testutil.WaitForAPI(t)
	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	user := testutil.RegisterAgent(t, "feedback-meta-"+suffix+"@test.com", "", "")
	author := testutil.RegisterAgent(t, "feedback-author-"+suffix+"@test.com", "", "")
	agentID := testutil.MustID(t, user["agent_id"], "agent_id")
	authorID := testutil.MustID(t, author["agent_id"], "agent_id")
	item := time.Now().UnixNano() / 1000
	for id, owner := range map[int64]int64{item: authorID, item + 1: agentID} {
		_, err := testutil.TestDB.Exec(`INSERT INTO raw_items(item_id,author_agent_id,raw_content,created_at) VALUES($1,$2,$3,$4)`, id, owner, fmt.Sprint("Feedback fixture ", id), time.Now().UnixMilli())
		require.NoError(t, err)
		_, err = testutil.TestDB.Exec(`INSERT INTO processed_items(item_id,status,summary,broadcast_type,updated_at) VALUES($1,3,'Feedback fixture','info',$2)`, id, time.Now().UnixMilli())
		require.NoError(t, err)
		_, err = testutil.TestDB.Exec(`INSERT INTO item_stats(item_id,author_agent_id,created_at,updated_at) VALUES($1,$2,$3,$3)`, id, owner, time.Now().UnixMilli())
		require.NoError(t, err)
	}
	t.Cleanup(func() {
		for _, table := range []string{"feedback_logs", "replay_logs", "followup_labels", "processed_items", "raw_items"} {
			_, err := testutil.TestDB.Exec("DELETE FROM "+table+" WHERE item_id IN ($1,$2)", item, item+1)
			if err != nil {
				t.Error(err)
			}
		}
		_, err := testutil.TestDB.Exec(`DELETE FROM agent_feedback_health_daily WHERE agent_id=$1`, agentID)
		if err != nil {
			t.Error(err)
		}
	})
	return user["token"].(string), agentID, item, item + 1
}

func feedbackMetadataRequest(method, path, token string, body any) (map[string]any, int, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequest(method, testutil.BaseURL+path, bytes.NewReader(raw))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-CLI-Ver", "0.6.10")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()
	var result map[string]any
	err = json.NewDecoder(response.Body).Decode(&result)
	return result, response.StatusCode, err
}

func TestFeedbackMetadataAndIdempotency(t *testing.T) {
	token, agent, item, own := feedbackMetadataFixture(t)
	request := func(body any) map[string]any {
		t.Helper()
		result, status, err := feedbackMetadataRequest("POST", "/api/v1/items/feedback", token, body)
		require.NoError(t, err)
		require.Equal(t, 200, status, result)
		require.EqualValues(t, 0, result["code"], result)
		return result["data"].(map[string]any)
	}
	id := strconv.FormatInt(item, 10)
	legacy := request(map[string]any{"items": []any{map[string]any{"item_id": id, "score": 0}}})
	require.EqualValues(t, 1, legacy["processed_count"])
	require.EqualValues(t, 0, legacy["skipped_count"])
	require.NotContains(t, legacy, "skipped_reasons")
	require.Equal(t, []any{map[string]any{"item_id": id, "status": "accepted"}}, legacy["results"])
	require.Eventually(t, func() bool {
		var n int
		return testutil.TestDB.QueryRow(`SELECT count(*) FROM feedback_logs WHERE agent_id=$1`, agent).Scan(&n) == nil && n == 1
	}, 10*time.Second, 50*time.Millisecond)
	var key, source, contract string
	var surfaced sql.NullBool
	require.NoError(t, testutil.TestDB.QueryRow(`SELECT client_key,impression_source,contract_version,surfaced FROM feedback_logs WHERE agent_id=$1`, agent).Scan(&key, &source, &contract, &surfaced))
	require.Empty(t, key)
	require.Empty(t, source)
	require.Empty(t, contract)
	require.False(t, surfaced.Valid)
	body := map[string]any{"contract_version": "feedback/v2", "items": []any{map[string]any{"item_id": id, "score": 2, "client_key": "report-1", "impression_id": "metadata-impression", "impression_source": "cli_inferred", "surfaced": false}}}
	request(body)
	request(body)
	var wg sync.WaitGroup
	type reply struct {
		body   map[string]any
		status int
		err    error
	}
	replies := make(chan reply, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, s, e := feedbackMetadataRequest("POST", "/api/v1/items/feedback", token, body)
			replies <- reply{r, s, e}
		}()
	}
	wg.Wait()
	close(replies)
	for r := range replies {
		require.NoError(t, r.err)
		require.Equal(t, 200, r.status, r.body)
	}
	// Wait until all accepted stream messages have been handled, including duplicates.
	redis := testutil.GetTestRedis()
	info, err := redis.XInfoStream(context.Background(), itemstats.StreamName).Result()
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		groups, e := redis.XInfoGroups(context.Background(), itemstats.StreamName).Result()
		if e != nil {
			return false
		}
		for _, g := range groups {
			if g.Name == itemstats.GroupName && g.Pending == 0 && g.LastDeliveredID == info.LastGeneratedID {
				return true
			}
		}
		return false
	}, 10*time.Second, 50*time.Millisecond)
	var n int
	require.NoError(t, testutil.TestDB.QueryRow(`SELECT count(*) FROM feedback_logs WHERE agent_id=$1 AND client_key='report-1'`, agent).Scan(&n))
	require.Equal(t, 1, n)
	require.NoError(t, testutil.TestDB.QueryRow(`SELECT client_key,impression_source,contract_version,surfaced FROM feedback_logs WHERE agent_id=$1 AND client_key='report-1'`, agent).Scan(&key, &source, &contract, &surfaced))
	require.Equal(t, "cli_inferred", source)
	require.Equal(t, "feedback/v2", contract)
	require.True(t, surfaced.Valid)
	require.False(t, surfaced.Bool)
	var zero, two int
	require.NoError(t, testutil.TestDB.QueryRow(`SELECT score_0_count,score_2_count FROM item_stats WHERE item_id=$1`, item).Scan(&zero, &two))
	require.Equal(t, 1, zero)
	require.Equal(t, 1, two)
	mixed := request(map[string]any{"items": []any{map[string]any{"item_id": id, "score": 1, "client_key": "mixed"}, map[string]any{"item_id": strconv.FormatInt(own, 10), "score": 1}, map[string]any{"item_id": id, "score": 1, "client_key": "invalid/key"}}})
	require.EqualValues(t, 1, mixed["processed_count"])
	require.EqualValues(t, 2, mixed["skipped_count"])
	results := mixed["results"].([]any)
	require.Equal(t, "accepted", results[0].(map[string]any)["status"])
	require.Equal(t, "own item "+strconv.FormatInt(own, 10), results[1].(map[string]any)["reason"])
	require.Equal(t, "invalid client_key for item "+id, results[2].(map[string]any)["reason"])
	before, err := redis.XLen(context.Background(), itemstats.StreamName).Result()
	require.NoError(t, err)
	body["contract_version"] = "invalid version"
	result, status, err := feedbackMetadataRequest("POST", "/api/v1/items/feedback", token, body)
	require.NoError(t, err)
	require.Equal(t, 400, status)
	require.Equal(t, "invalid contract_version", result["msg"])
	after, err := redis.XLen(context.Background(), itemstats.StreamName).Result()
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Eventually(t, func() bool {
		var n int
		return testutil.TestDB.QueryRow(`SELECT count(*) FROM feedback_logs WHERE agent_id=$1 AND client_key='mixed'`, agent).Scan(&n) == nil && n == 1
	}, 10*time.Second, 50*time.Millisecond)
}

func TestFeedbackHealthReportE2E(t *testing.T) {
	token, agent, _, _ := feedbackMetadataFixture(t)
	for _, attempts := range []int{3, 5} {
		result, status, err := feedbackMetadataRequest("PUT", "/api/v1/agents/me/settings", token, map[string]any{"feedback_health": map[string]any{"window_ms": 3600000, "pulls": 2, "delivered_items": 10, "scored_items": 8, "status_runs": 1, "feedback_attempts": attempts, "feedback_failures": 1, "queue_drops": 0, "queue_depth": 1, "oldest_queued_age_ms": 100}})
		require.NoError(t, err)
		require.Equal(t, 200, status, result)
	}
	var n, attempts int
	var at int64
	var version, day string
	require.NoError(t, testutil.TestDB.QueryRow(`SELECT count(*),max(feedback_attempts),max(reported_at),max(cli_version),max(day)::text FROM agent_feedback_health_daily WHERE agent_id=$1`, agent).Scan(&n, &attempts, &at, &version, &day))
	require.Equal(t, 1, n)
	require.Equal(t, 5, attempts)
	require.Equal(t, "0.6.10", version)
	require.Equal(t, time.UnixMilli(at).In(time.FixedZone("CST", 8*3600)).Format("2006-01-02"), day)
	database, err := gorm.Open(postgres.Open(config.Load().PgDSN), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := database.DB()
	require.NoError(t, err)
	defer sqlDB.Close()
	require.NoError(t, apidal.ReportFeedbackHealth(database, agent, at-1, "old", &apidal.FeedbackHealthReport{WindowMS: 1}))
	require.NoError(t, testutil.TestDB.QueryRow(`SELECT feedback_attempts FROM agent_feedback_health_daily WHERE agent_id=$1`, agent).Scan(&attempts))
	require.Equal(t, 5, attempts)
	result, status, err := feedbackMetadataRequest("PUT", "/api/v1/agents/me/settings", token, map[string]any{"feedback_health": map[string]any{"window_ms": 1, "feedback_attempts": 1, "feedback_failures": 2}})
	require.NoError(t, err)
	require.Equal(t, 400, status, result)
	require.Equal(t, "invalid feedback_health", result["msg"])
	result, status, err = feedbackMetadataRequest("PUT", "/api/v1/agents/me/settings", token, map[string]any{})
	require.NoError(t, err)
	require.Equal(t, 200, status, result)
	require.NoError(t, testutil.TestDB.QueryRow(`SELECT reported_at FROM agent_feedback_health_daily WHERE agent_id=$1`, agent).Scan(&n))
	require.Equal(t, at, int64(n))
}

func TestFeedbackReportingComplianceE2E(t *testing.T) {
	token, agent, item, _ := feedbackMetadataFixture(t)
	silentToken, silent, _ := testutil.LoginAndGetToken(t, fmt.Sprintf("feedback-silent-%d@test.com", item))
	now := time.Now()
	day := now.In(time.FixedZone("CST", 8*3600)).Format("2006-01-02")
	cutoff := now.Add(72 * time.Hour)
	snapshot := func() map[string]int64 {
		t.Helper()
		require.NoError(t, recommendationmetrics.RefreshComplianceDay(context.Background(), testutil.TestDB, day, cutoff))
		var raw []byte
		require.NoError(t, testutil.TestDB.QueryRow(`SELECT to_jsonb(d)-ARRAY['day','dimension','value','snapshot_at','schema_version'] FROM feedback_compliance_daily d WHERE day=$1 AND dimension='all'`, day).Scan(&raw))
		counts := map[string]int64{}
		require.NoError(t, json.Unmarshal(raw, &counts))
		return counts
	}
	before := snapshot()
	t.Cleanup(func() {
		testutil.TestDB.Exec(`DELETE FROM feedback_compliance_daily WHERE day=$1`, day)
		testutil.TestDB.Exec(`DELETE FROM agent_feedback_health_daily WHERE agent_id=$1`, silent)
	})
	impression := fmt.Sprint("reporting-", item)
	for i, id := range []int64{agent, silent} {
		_, err := testutil.TestDB.Exec(`INSERT INTO replay_logs(id,agent_id,item_id,impression_id,served_at,created_at,delivered,source_kind,pipeline_version,sample_schema_version,request_mode,item_features)
 VALUES($1,$2,$3,$4,$5,$5,true,'broadcast','need_search_v1',2,'recommendation','{"search":{"context":{"input_origin":"agent_context"}}}')`, item+int64(i), id, item, impression+strconv.Itoa(i), now.Add(-time.Second).UnixMilli())
		require.NoError(t, err)
	}
	result, status, err := feedbackMetadataRequest("POST", "/api/v1/items/feedback", token, map[string]any{"contract_version": "feedback/v2", "items": []any{map[string]any{"item_id": strconv.FormatInt(item, 10), "score": 2, "client_key": "reporting-key", "surfaced": true, "impression_id": impression + "0", "impression_source": "cli_inferred"}}})
	require.NoError(t, err)
	require.Equal(t, 200, status, result)
	result, status, err = feedbackMetadataRequest("POST", "/api/v1/items/events", token, map[string]any{"events": []any{map[string]any{"item_id": strconv.FormatInt(item, 10), "kind": "surface", "impression_id": impression + "0", "dedup_key": impression, "ts": now.UnixMilli()}}})
	require.NoError(t, err)
	require.Equal(t, 200, status, result)
	for i, tok := range []string{token, silentToken} {
		result, status, err = feedbackMetadataRequest("PUT", "/api/v1/agents/me/settings", tok, map[string]any{"feedback_health": map[string]any{"window_ms": 1000, "feedback_attempts": i + 2, "feedback_failures": 1, "queue_drops": i + 1}})
		require.NoError(t, err)
		require.Equal(t, 200, status, result)
	}
	require.Eventually(t, func() bool {
		var scores, surfaces int
		err := testutil.TestDB.QueryRow(`SELECT (SELECT count(*) FROM feedback_logs WHERE agent_id=$1 AND client_key='reporting-key'),(SELECT count(*) FROM followup_labels WHERE agent_id=$1 AND impression_id=$2)`, agent, impression+"0").Scan(&scores, &surfaces)
		return err == nil && scores == 1 && surfaces == 1
	}, 10*time.Second, 50*time.Millisecond)
	after := snapshot()
	for key, want := range map[string]int64{"feedback_events_keyed": 1, "feedback_events_cli_inferred": 1, "surface_declared_known_exposures": 1, "surface_declared_exposures": 1, "surface_agreement_exposures": 1, "health_reported_agents": 2, "silent_health_reported_agents": 1, "silent_attempting_agents": 1, "health_feedback_attempts": 5, "health_feedback_failures": 2, "health_queue_drops": 3} {
		require.Equal(t, want, after[key]-before[key], key)
	}
}
