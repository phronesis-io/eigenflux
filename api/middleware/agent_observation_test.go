package middleware

import (
	"context"
	"testing"

	"eigenflux_server/api/dal"
	"github.com/cloudwego/hertz/pkg/app"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestAgentObservationSuccessAndReadBoundaries(t *testing.T) {
	tests := []struct {
		name, method, path, body, origin, cli string
		status                                int
		legacy, want                          bool
	}{
		{name: "V2 feed", method: "POST", path: "/api/v2/feed", body: `{"data":{"items":[]}}`, status: 200, want: true},
		{name: "heartbeat", method: "POST", path: "/api/v2/runtime/heartbeat", body: `{"data":{}}`, status: 200, want: true},
		{name: "broadcast", method: "POST", path: "/api/v2/broadcasts", body: `{"code":0}`, status: 200, want: true},
		{name: "message", method: "POST", path: "/api/v2/pm/messages", body: `{"code":0}`, status: 200, want: true},
		{name: "Attention response", method: "POST", path: "/api/v2/agent-attention-items/:attention_id/respond", body: `{"data":{}}`, status: 200, want: true},
		{name: "command completion", method: "POST", path: "/api/v2/agent-commands/:command_id/complete", body: `{"data":{}}`, status: 200, want: true},
		{name: "feed events", method: "POST", path: "/api/v2/feed/events:batch", body: `{"code":0}`, status: 200, want: true},
		{name: "broadcast deletion", method: "DELETE", path: "/api/v2/broadcasts/:item_id", body: `{"code":0}`, status: 200, want: true},
		{name: "unfriend", method: "POST", path: "/api/v2/relations/friends/unfriend", body: `{"code":0}`, status: 200, want: true},
		{name: "Console Attention response", method: "POST", path: "/api/v2/console/attention-items/:attention_id/respond", body: `{"data":{}}`, status: 200},
		{name: "business failure", method: "POST", path: "/api/v2/feed", body: `{"code":500}`, status: 200},
		{name: "V2 failure", method: "POST", path: "/api/v2/feed", body: `{"error":{"code":"FAILED"}}`, status: 200},
		{name: "HTTP failure", method: "POST", path: "/api/v2/feed", body: `{"code":0}`, status: 500},
		{name: "profile read", method: "GET", path: "/api/v2/agent-profile", body: `{"data":{}}`, status: 200},
		{name: "settings read", method: "GET", path: "/api/v2/agent-settings", body: `{"data":{}}`, status: 200},
		{name: "console view", method: "GET", path: "/api/v2/console/today", body: `{"data":{}}`, status: 200},
		{name: "legacy browser", method: "GET", path: "/api/v1/items/feed", body: `{"code":0}`, cli: "0.0.42", origin: "https://console.eigenflux.ai", legacy: true, status: 200},
		{name: "legacy unknown caller", method: "GET", path: "/api/v1/items/feed", body: `{"code":0}`, legacy: true, status: 200},
		{name: "legacy CLI", method: "GET", path: "/api/v1/items/feed", body: `{"code":0}`, cli: "0.0.42", legacy: true, status: 200, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
			if err != nil {
				t.Fatal(err)
			}
			if err := database.AutoMigrate(&dal.AgentSettings{}); err != nil {
				t.Fatal(err)
			}
			c := app.NewContext(0)
			c.Request.SetMethod(tt.method)
			c.Request.SetRequestURI(tt.path)
			c.Request.Header.Set("X-Client-Host", "workbuddy/5.5")
			c.Request.Header.Set("X-Client-Mode", "skill")
			if tt.cli != "" {
				c.Request.Header.Set("X-CLI-Ver", tt.cli)
			}
			if tt.origin != "" {
				c.Request.Header.Set("Origin", tt.origin)
			}
			c.Response.SetStatusCode(tt.status)
			c.Response.SetBodyString(tt.body)
			ObserveSuccessfulAgentRequest(context.Background(), c, database, 101, 100000, tt.legacy)
			var rows []dal.AgentSettings
			if err := database.Find(&rows).Error; err != nil {
				t.Fatal(err)
			}
			if !tt.want {
				if len(rows) != 0 {
					t.Fatalf("non-Agent activity changed settings: %+v", rows)
				}
				return
			}
			if len(rows) != 1 || rows[0].RuntimeName != "workbuddy" || rows[0].Mode != "skill" || rows[0].LastActivityAt != 100000 {
				t.Fatalf("successful activity was not observed: %+v", rows)
			}
		})
	}
}
