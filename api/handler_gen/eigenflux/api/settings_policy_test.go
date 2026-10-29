package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"eigenflux_server/api/dal"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/reqinfo"

	"github.com/bytedance/gopkg/cloud/metainfo"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestSettingsPolicyIsAnAuthenticatedObservation(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := database.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	previous := db.DB
	db.DB = database
	t.Cleanup(func() { db.DB = previous; _ = sqlDB.Close() })
	require.NoError(t, database.AutoMigrate(&dal.AgentSettings{}))
	require.NoError(t, database.Exec(`CREATE TABLE telemetry_events_v2 (
event_id TEXT PRIMARY KEY, agent_id INTEGER, event_type TEXT, properties JSONB,
event_at INTEGER, created_at INTEGER, expires_at INTEGER)`).Error)
	for _, tc := range []struct {
		id     int64
		body   string
		status int
		rows   int64
	}{
		{41, `{}`, http.StatusOK, 0},
		{42, `{"auto_cli_update":true,"agent_id":999}`, http.StatusOK, 1},
		{43, `{"auto_cli_update":false,"agent_id":999}`, http.StatusOK, 2},
		{45, `{"auto_cli_update":null}`, http.StatusOK, 2},
		{44, `{"auto_cli_update":"false"}`, http.StatusBadRequest, 2},
	} {
		c := app.NewContext(0)
		c.Request.SetBodyString(tc.body)
		c.Set("agent_id", tc.id)
		ctx := metainfo.WithPersistentValue(context.Background(), reqinfo.KeyClientHost, "codex")
		ctx = metainfo.WithPersistentValue(ctx, reqinfo.KeyClientMode, "skill")
		ctx = metainfo.WithPersistentValue(ctx, reqinfo.KeyCLIVer, "0.7.6")
		PutMySettings(ctx, c)
		require.Equal(t, tc.status, c.Response.StatusCode(), "%s", c.Response.Body())
		var count int64
		require.NoError(t, database.Table("telemetry_events_v2").Count(&count).Error)
		require.Equal(t, tc.rows, count)
	}
	unauthenticated := app.NewContext(0)
	unauthenticated.Request.SetBodyString(`{"auto_cli_update":true,"agent_id":999}`)
	PutMySettings(context.Background(), unauthenticated)
	require.Equal(t, http.StatusUnauthorized, unauthenticated.Response.StatusCode())
	var count int64
	require.NoError(t, database.Table("telemetry_events_v2").Count(&count).Error)
	require.Equal(t, int64(2), count)
	var rows []struct {
		AgentID    int64
		Properties string
	}
	require.NoError(t, database.Table("telemetry_events_v2").Order("agent_id").Find(&rows).Error)
	for i, row := range rows {
		require.Equal(t, int64(42+i), row.AgentID)
		var props map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(row.Properties), &props))
		require.Equal(t, i == 0, props["auto_cli_update"])
		require.Equal(t, "codex", props["host"])
		require.Equal(t, "skill", props["mode"])
	}
}

func TestSettingsReadOnlyAdministratorCLIUpdatePolicy(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := database.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	previous := db.DB
	db.DB = database
	t.Cleanup(func() { db.DB = previous; _ = sqlDB.Close() })
	require.NoError(t, database.AutoMigrate(&dal.AgentSettings{}))
	require.NoError(t, database.Create(&dal.AgentSettings{AgentID: 42, FeedPollIntervalUserSet: true, FeedPollInterval: 300}).Error)
	require.NoError(t, database.Exec(`CREATE TABLE telemetry_events_v2 (
event_id TEXT PRIMARY KEY, agent_id INTEGER, event_type TEXT, properties JSONB,
event_at INTEGER, created_at INTEGER, expires_at INTEGER)`).Error)
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"", true}, {"false", false}, {"true", true},
	} {
		t.Setenv("ENABLE_CLI_AUTO_UPDATE", tc.value)
		require.Equal(t, tc.want, config.Load().EnableCLIAutoUpdate)
		write := app.NewContext(0)
		write.Set("agent_id", int64(42))
		body, err := json.Marshal(map[string]bool{"auto_cli_update": !tc.want})
		require.NoError(t, err)
		write.Request.SetBody(body)
		PutMySettings(context.Background(), write)
		require.Equal(t, http.StatusOK, write.Response.StatusCode(), "%s", write.Response.Body())
		c := app.NewContext(0)
		c.Set("agent_id", int64(42))
		GetMySettings(context.Background(), c)
		require.Equal(t, http.StatusOK, c.Response.StatusCode(), "%s", c.Response.Body())
		var response struct {
			Data struct {
				AutoCLIUpdate bool `json:"auto_cli_update"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(c.Response.Body(), &response))
		require.Equal(t, tc.want, response.Data.AutoCLIUpdate)
	}
}
