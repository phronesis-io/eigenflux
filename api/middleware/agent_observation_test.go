package middleware

import (
	"context"
	"strings"
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
		{name: "Need create", method: "POST", path: "/api/v2/need-inputs", body: `{"code":0,"data":{}}`, status: 201, want: true},
		{name: "Need read", method: "GET", path: "/api/v2/need-inputs/:need_input_id", body: `{"data":{}}`, status: 200},
		{name: "Need list", method: "GET", path: "/api/v2/need-inputs", body: `{"data":{}}`, status: 200},
		{name: "Need invalid", method: "POST", path: "/api/v2/need-inputs", body: `{"error":{}}`, status: 400},
		{name: "discovery search", method: "POST", path: "/api/v2/discovery/search", body: `{"code":0}`, status: 200, want: true},
		{name: "discovery recommendation", method: "POST", path: "/api/v2/discovery/recommendations", body: `{"code":0}`, status: 200, want: true},
		{name: "metadata read", method: "GET", path: "/api/v2/agents/me", body: `{"code":0}`, status: 200},
		{name: "failed discovery", method: "POST", path: "/api/v2/discovery/search", body: `{"code":503}`, status: 503},
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

func TestPassiveCLIObservationOnlyChangesVersion(t *testing.T) {
	for _, path := range []string{"/api/v2/agents/me/settings", "/api/v2/notifications/pending"} {
		t.Run(path, func(t *testing.T) {
			database, before := passiveObservationDB(t)
			c := passiveObservationRequest(path, "1.0.9")
			ObserveSuccessfulAgentRequest(context.Background(), c, database, before.AgentID, 200000, false)
			var after dal.AgentSettings
			if err := database.First(&after, "agent_id = ?", before.AgentID).Error; err != nil {
				t.Fatal(err)
			}
			if after.CLIVersion != "1.0.9" || after.RuntimeReportedAt != 200000 || after.UpdatedAt <= before.UpdatedAt {
				t.Fatalf("CLI observation not recorded: %+v", after)
			}
			first := after
			ObserveSuccessfulAgentRequest(context.Background(), c, database, before.AgentID, 200001, false)
			if err := database.First(&after, "agent_id = ?", before.AgentID).Error; err != nil {
				t.Fatal(err)
			}
			if after != first {
				t.Fatalf("unchanged passive poll was not coalesced: before=%+v after=%+v", first, after)
			}
			// Compare every stored field to catch accidental activity, product, mode,
			// model, compatibility or user-setting updates from passive polling.
			after.CLIVersion, after.RuntimeReportedAt, after.UpdatedAt = before.CLIVersion, before.RuntimeReportedAt, before.UpdatedAt
			if after != before {
				t.Fatalf("passive observation changed unrelated settings:\nbefore=%+v\nafter=%+v", before, after)
			}

			// A delayed request must not overwrite a more recent explicit report.
			if _, err := dal.ObserveRuntime(database, before.AgentID, dal.RuntimeObservation{CLIVersion: "1.0.10", ObservedAt: 300000, Explicit: true}); err != nil {
				t.Fatal(err)
			}
			ObserveSuccessfulAgentRequest(context.Background(), c, database, before.AgentID, 250000, false)
			if err := database.First(&after, "agent_id = ?", before.AgentID).Error; err != nil {
				t.Fatal(err)
			}
			if after.CLIVersion != "1.0.10" || after.RuntimeReportedAt != 300000 || after.LastActivityAt != before.LastActivityAt {
				t.Fatalf("delayed passive version bypassed report fence: %+v", after)
			}
		})
	}
}

func TestPassiveCLIObservationBoundaries(t *testing.T) {
	tests := []struct {
		name, path, method, version, header, body string
		status                                    int
	}{
		{name: "missing version"},
		{name: "oversized version", version: strings.Repeat("v", 33)},
		{name: "control character", version: "1.0.9\t"},
		{name: "invalid UTF8", version: "1.0.\xff"},
		{name: "browser origin", version: "1.0.9", header: "Origin"},
		{name: "browser fetch", version: "1.0.9", header: "Sec-Fetch-Site"},
		{name: "HTTP failure", version: "1.0.9", status: 500},
		{name: "business failure", version: "1.0.9", body: `{"code":500}`},
		{name: "V2 error", version: "1.0.9", body: `{"error":{"code":"FAILED"}}`},
		{name: "invalid response", version: "1.0.9", body: "invalid"},
		{name: "unrelated metadata read", version: "1.0.9", path: "/api/v2/agents/me"},
		{name: "legacy settings read", version: "1.0.9", path: "/api/v1/agents/me/settings"},
		{name: "different method", version: "1.0.9", method: "POST"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			database, before := passiveObservationDB(t)
			path := tt.path
			if path == "" {
				path = "/api/v2/agents/me/settings"
			}
			c := passiveObservationRequest(path, tt.version)
			if tt.method != "" {
				c.Request.SetMethod(tt.method)
			}
			if tt.header != "" {
				c.Request.Header.Set(tt.header, "https://console.eigenflux.ai")
			}
			if tt.status != 0 {
				c.Response.SetStatusCode(tt.status)
			}
			if tt.body != "" {
				c.Response.SetBodyString(tt.body)
			}
			ObserveSuccessfulAgentRequest(context.Background(), c, database, before.AgentID, 200000, false)
			var after dal.AgentSettings
			if err := database.First(&after, "agent_id = ?", before.AgentID).Error; err != nil {
				t.Fatal(err)
			}
			if after != before {
				t.Fatalf("excluded request changed settings:\nbefore=%+v\nafter=%+v", before, after)
			}
		})
	}
}

func passiveObservationDB(t *testing.T) (*gorm.DB, dal.AgentSettings) {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AutoMigrate(&dal.AgentSettings{}); err != nil {
		t.Fatal(err)
	}
	before := dal.AgentSettings{AgentID: 102, RuntimeName: "workbuddy", RuntimeVersion: "5.9.3", Mode: "skill", Model: "known-model", CLIVersion: "1.0.5", DeviceName: "known-device", LastActivityAt: 100000, RuntimeReportedAt: 110000, UpdatedAt: 123}
	if err := database.Create(&before).Error; err != nil {
		t.Fatal(err)
	}
	return database, before
}

func passiveObservationRequest(path, version string) *app.RequestContext {
	c := app.NewContext(0)
	c.Request.SetMethod("GET")
	c.Request.SetRequestURI(path)
	c.Request.Header.Set("X-CLI-Ver", version)
	// These deliberately different headers must have no effect on passive reads.
	c.Request.Header.Set("X-Client-Host", "openclaw/2026.11")
	c.Request.Header.Set("X-Client-Mode", "plugin")
	c.Request.Header.Set("X-Client-Model", "different-model")
	c.Response.SetStatusCode(200)
	c.Response.SetBodyString(`{"code":0,"data":{}}`)
	return c
}
