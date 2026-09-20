package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/itemstats"
	"eigenflux_server/pkg/mq"

	"github.com/alicebob/miniredis/v2"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestBatchFeedbackSkipsAuthorOwnItems(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := database.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	previousDB := db.DB
	db.DB = database
	t.Cleanup(func() {
		db.DB = previousDB
		_ = sqlDB.Close()
	})
	require.NoError(t, database.Exec(`CREATE TABLE raw_items (item_id INTEGER PRIMARY KEY, author_agent_id INTEGER NOT NULL, raw_content TEXT NOT NULL, created_at INTEGER NOT NULL)`).Error)
	require.NoError(t, database.Exec(`INSERT INTO raw_items (item_id, author_agent_id, raw_content, created_at) VALUES (7, 42, 'own broadcast', 1), (8, 99, 'someone else', 1)`).Error)

	mr, err := miniredis.Run()
	require.NoError(t, err)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	previousRDB := mq.RDB
	mq.RDB = client
	t.Cleanup(func() {
		mq.RDB = previousRDB
		_ = client.Close()
		mr.Close()
	})

	c := app.NewContext(0)
	c.Request.Header.SetMethod(http.MethodPost)
	c.Request.Header.SetContentTypeBytes([]byte("application/json"))
	body := `{"items":[{"item_id":"7","score":2},{"item_id":"8","score":1},{"item_id":"404","score":1}]}`
	c.Request.SetBodyString(body)
	c.Request.Header.SetContentLength(len(body))
	c.Set("agent_id", int64(42))

	BatchFeedback(context.Background(), c)

	require.Equal(t, http.StatusOK, c.Response.StatusCode(), "body: %s", c.Response.Body())
	var result struct {
		Code int               `json:"code"`
		Data BatchFeedbackData `json:"data"`
	}
	require.NoError(t, json.Unmarshal(c.Response.Body(), &result))
	require.Zero(t, result.Code, "body: %s", c.Response.Body())
	require.Equal(t, 2, result.Data.ProcessedCount)
	require.Equal(t, 1, result.Data.SkippedCount)
	require.Equal(t, []string{"own item 7"}, result.Data.SkippedReasons)

	events, err := client.XRange(context.Background(), itemstats.StreamName, "-", "+").Result()
	require.NoError(t, err)
	require.Len(t, events, 2, "only the non-author scores may reach the stats stream")
	for _, event := range events {
		require.NotEqual(t, "7", event.Values["item_id"], "author feedback must not be published")
	}
}
