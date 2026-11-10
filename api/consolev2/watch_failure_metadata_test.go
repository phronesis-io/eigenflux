package consolev2

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestWatchFailureMetadataDoesNotRejectValidMaintenanceBatch(t *testing.T) {
	for _, tc := range []struct{ name, header, want string }{
		{"valid", `{"watch-id":"skills_target_unresolved"}`, "skills_target_unresolved"},
		{"legacy", "", ""},
		{"malformed", "{", ""},
		{"wrong_type", `{"watch-id":42}`, ""},
		{"invalid_reason", `{"watch-id":"/private/path?token=secret"}`, ""},
		{"unknown_event", `{"other":"skills_target_unresolved"}`, ""},
		{"wrong_category", `{"watch-id":"future_reason"}`, ""},
		{"oversized", strings.Repeat("a", 8193), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now()
			failed := validMaintenanceEvent()
			failed.Component, failed.Phase, failed.Result, failed.ErrorCode, failed.RunningVersion, failed.ToVersion = "watch", "skills", "failed", "skills_failed", "", ""
			failed.EventID = strings.Repeat("c", 64)
			failed.OuterLoop = &outerLoopObservation{Environment: outerLoopEnvironment{OS: "windows", Arch: "amd64", Container: "unknown", WSL: "not_applicable"}}
			ordinary := validMaintenanceEvent()
			header := strings.ReplaceAll(tc.header, "watch-id", failed.EventID)
			db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
			if err != nil {
				t.Fatal(err)
			}
			sqlDB, _ := db.DB()
			defer sqlDB.Close()
			var rows []telemetryEventRequest
			writes := 0
			err = db.Callback().Raw().Replace("gorm:raw", func(tx *gorm.DB) {
				writes++
				if len(tx.Statement.Vars) != 4 {
					t.Errorf("unexpected persistence vars: %+v", tx.Statement.Vars)
					return
				}
				encoded, ok := tx.Statement.Vars[3].(string)
				if !ok {
					t.Errorf("missing encoded rows")
					return
				}
				if err := json.Unmarshal([]byte(encoded), &rows); err != nil {
					t.Error(err)
				}
				tx.RowsAffected = int64(len(rows))
			})
			if err != nil {
				t.Fatal(err)
			}
			svc := &Service{db: db, telemetryRates: map[string]telemetryRateState{}}
			h := server.New()
			h.POST("/maintenance", func(ctx context.Context, c *app.RequestContext) {
				c.Set("agent_id", int64(42))
				svc.recordMaintenanceBatch(ctx, c)
			})
			status, _, _ := performJSON(t, h, "POST", "/maintenance", map[string]interface{}{"events": []maintenanceEvent{failed, ordinary}}, ut.Header{Key: watchFailuresHeader, Value: header})
			if status != 202 || writes != 1 || len(rows) != 2 {
				t.Fatalf("metadata blocked batch: status=%d writes=%d rows=%+v", status, writes, rows)
			}
			raw, _ := json.Marshal(rows[0].Properties)
			var got struct {
				OuterLoop outerLoopObservation `json:"outer_loop"`
			}
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if got.OuterLoop.FailureReason != tc.want {
				t.Fatalf("reason=%q want %q", got.OuterLoop.FailureReason, tc.want)
			}
			if failed.OuterLoop.FailureReason != "" {
				t.Fatal("original event pointer mutated")
			}
			if err := validateMaintenanceEvent(mergeWatchFailureMetadata(failed, tc.want, now), now); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestWatchFailureMetadataBoundsAndIncompatibleEvent(t *testing.T) {
	entries := map[string]string{}
	for i := 0; i < 51; i++ {
		entries[fmt.Sprint(i)] = "skills_target_unresolved"
	}
	raw, _ := json.Marshal(entries)
	if parseMaintenanceFailureMetadata(string(raw)) != nil {
		t.Fatal("oversized map accepted")
	}
	e := validMaintenanceEvent()
	if got := mergeWatchFailureMetadata(e, "skills_target_unresolved", time.Now()); got.OuterLoop != nil {
		t.Fatal("detail added to non-watch event")
	}
}
