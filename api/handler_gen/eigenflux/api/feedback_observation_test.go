package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"

	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/mq"
	"github.com/alicebob/miniredis/v2"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func observeFeedback(t *testing.T, body string) (*app.RequestContext, []map[string]any) {
	t.Helper()
	var logs bytes.Buffer
	prior := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(prior)
	c := app.NewContext(0)
	c.Set("agent_id", int64(42))
	c.SetFullPath("/api/v2/feed/feedback")
	c.Request.Header.SetMethod(http.MethodPost)
	c.Request.Header.SetContentTypeBytes([]byte("application/json"))
	c.Request.SetBodyString(body)
	c.Request.Header.SetContentLength(len(body))
	BatchFeedback(context.Background(), c)
	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var v map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &v))
		records = append(records, v)
	}
	return c, records
}
func TestFeedbackBindingAndContractObservation(t *testing.T) {
	for _, tc := range []struct{ body, stage, class string }{
		{`{"items":[{"item_id":123,"score":1}]}`, "bind", "type_mismatch"},
		{`{"items":`, "bind", "syntax"},
		{`{"items":[{"item_id":"7","score":1}],"contract_version":"private secret value"}`, "contract", "invalid_contract_version"},
	} {
		c, logs := observeFeedback(t, tc.body)
		require.Equal(t, 400, c.Response.StatusCode())
		require.Equal(t, tc.stage, logs[0]["stage"])
		require.Equal(t, tc.class, logs[0]["error_class"])
		require.Equal(t, float64(42), logs[0]["agentID"])
		raw, _ := json.Marshal(logs)
		require.NotContains(t, string(raw), "private secret value")
		require.NotContains(t, string(raw), "body")
	}
}

type failFeedbackPublish struct{ calls int }

func (h *failFeedbackPublish) DialHook(next redis.DialHook) redis.DialHook {
	return func(ctx context.Context, network, addr string) (net.Conn, error) { return next(ctx, network, addr) }
}
func (h *failFeedbackPublish) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}
func (h *failFeedbackPublish) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if cmd.Name() == "xadd" && len(cmd.Args()) > 1 && cmd.Args()[1] == "stream:item:stats" {
			h.calls++
			if h.calls == 2 {
				return errors.New("publisher unavailable")
			}
		}
		return next(ctx, cmd)
	}
}
func TestFeedbackMixedAndPartialPublishObservation(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := database.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	oldDB, oldRedis := db.DB, mq.RDB
	db.DB = database
	t.Cleanup(func() { db.DB = oldDB; mq.RDB = oldRedis; sqlDB.Close() })
	require.NoError(t, database.Exec(`CREATE TABLE raw_items(item_id INTEGER PRIMARY KEY,author_agent_id INTEGER); INSERT INTO raw_items VALUES(7,42),(8,99),(9,99)`).Error)
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	mq.RDB = client
	c, logs := observeFeedback(t, `{"items":[{"item_id":"7","score":1},{"item_id":"8","score":1,"client_key":"key","impression_id":"imp"},{"item_id":"404","score":1},{"item_id":"bad","score":1},{"item_id":"8","score":3},{"item_id":"8","score":1,"client_key":"/"},{"item_id":"8","score":1,"impression_source":"bad"}]}`)
	require.Equal(t, 200, c.Response.StatusCode())
	summary := logs[len(logs)-1]
	require.Equal(t, "BatchFeedback result", summary["msg"])
	for _, key := range []string{"accepted", "own_item", "unknown_item", "invalid_item_id", "invalid_score", "invalid_client_key", "invalid_impression_source", "with_impression"} {
		require.Equal(t, float64(1), summary[key], key)
	}
	require.Equal(t, float64(2), summary["keyed"])
	client.AddHook(&failFeedbackPublish{})
	c, logs = observeFeedback(t, `{"items":[{"item_id":"8","score":1},{"item_id":"9","score":1}]}`)
	require.Equal(t, 500, c.Response.StatusCode())
	summary = logs[len(logs)-1]
	require.Equal(t, float64(1), summary["accepted"])
	require.Equal(t, float64(1), summary["publish_failed"])
	require.Equal(t, float64(1), summary["published_before_failure"])
}
