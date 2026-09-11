package dal

import (
	"context"
	"eigenflux_server/pkg/reqinfo"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestRuntimeObservationPostgresConcurrentFence(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN required for real PostgreSQL row-lock contract")
	}
	database, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	id := time.Now().UnixNano()
	now := time.Now().UnixMilli()
	if err := database.Exec(`INSERT INTO agents (agent_id,email,agent_name,bio,created_at,updated_at) VALUES (?,?,'Runtime fence','',?,?)`, id, fmt.Sprintf("runtime-fence-%d@example.test", id), now, now).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Exec("DELETE FROM agents WHERE agent_id = ?", id) })
	if _, err := ObserveRuntime(database, id, RuntimeObservation{Host: "openclaw/old", Mode: "plugin", ObservedAt: 100}); err != nil {
		t.Fatal(err)
	}
	// Hold an explicit transaction open while a delayed passive request contends
	// for the same row. PostgreSQL must re-read the committed fence after waiting.
	tx := database.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	if _, err := ObserveRuntime(tx, id, RuntimeObservation{Mode: "skill", ObservedAt: 500, Explicit: true}); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		r, e := ObserveRuntime(database, id, RuntimeObservation{Host: "openclaw/old", Mode: "plugin", ObservedAt: 400})
		if e == nil && r.Outcome != "stale" {
			e = fmt.Errorf("delayed request result=%+v", r)
		}
		done <- e
	}()
	<-started
	select {
	case err := <-done:
		t.Fatalf("observation did not wait for explicit transaction: %v", err)
	case <-time.After(40 * time.Millisecond):
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errors := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := ObserveRuntime(database, id, RuntimeObservation{Active: true, ObservedAt: int64(100000 + i*60000)})
			errors <- err
		}(i)
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	var row AgentSettings
	if err := database.First(&row, "agent_id = ?", id).Error; err != nil {
		t.Fatal(err)
	}
	if row.Mode != "skill" || row.ClientHost != "" || row.RuntimeReportedAt != 500 || row.LastActivityAt != 640000 {
		t.Fatalf("concurrent observation corrupted identity/activity: %+v", row)
	}
}

func TestRuntimeObservationPostgresRejectsMalformedOptionalMetadata(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN required for PostgreSQL metadata column limits")
	}
	database, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	id := time.Now().UnixNano()
	now := time.Now().UnixMilli()
	if err := database.Exec(`INSERT INTO agents (agent_id,email,agent_name,bio,created_at,updated_at) VALUES (?,?,'Runtime bounds','',?,?)`, id, fmt.Sprintf("runtime-bounds-%d@example.test", id), now, now).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Exec("DELETE FROM agents WHERE agent_id = ?", id) })
	if _, err := ObserveRuntime(database, id, RuntimeObservation{Host: "codex", CLIVersion: "0.0.44", Model: "known-model", ObservedAt: 100}); err != nil {
		t.Fatal(err)
	}
	for index, malformed := range []string{strings.Repeat("v", 33), strings.Repeat("v", 128), "0.0.\x0044", "0.0.44\t"} {
		timestamp := int64(200000 + index*60000)
		result, err := ObserveRuntime(database, id, RuntimeObservation{Host: "workbuddy/5.5.4", Mode: "skill", CLIVersion: malformed, Model: strings.Repeat("m", 129), ObservedAt: timestamp, Active: true})
		if err != nil {
			t.Fatalf("optional invalid header rolled back valid facts: %v", err)
		}
		var row AgentSettings
		if err := database.First(&row, "agent_id = ?", id).Error; err != nil {
			t.Fatal(err)
		}
		if row.RuntimeName != "workbuddy" || row.Mode != "skill" || row.LastActivityAt != timestamp || row.CLIVersion != "0.0.44" || row.Model != "known-model" {
			t.Fatalf("valid facts lost or invalid metadata stored: result=%+v row=%+v", result, row)
		}
	}
}

func TestRuntimeObservationPostgresHandoffUsesEntryFence(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN required for PostgreSQL handoff entry fence")
	}
	database, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	id := time.Now().UnixNano()
	now := time.Now().UnixMilli()
	if err := database.Exec(`INSERT INTO agents (agent_id,email,agent_name,bio,created_at,updated_at) VALUES (?,?,'Runtime handoff fence','',?,?)`, id, fmt.Sprintf("runtime-entry-%d@example.test", id), now, now).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Exec("DELETE FROM agents WHERE agent_id = ?", id) })
	entry := reqinfo.WithRequestStart(context.Background())
	latest := reqinfo.RequestStartedAt(entry) + 100
	if _, err := ObserveRuntime(database, id, RuntimeObservation{Host: "workbuddy/5.5.4", Mode: "skill", ObservedAt: latest, Explicit: true}); err != nil {
		t.Fatal(err)
	}
	if err := database.Model(&AgentSettings{}).Where("agent_id = ?", id).UpdateColumn("device_name", "current-device").Error; err != nil {
		t.Fatal(err)
	}
	err = UpdateHandoffClientIdentity(database.WithContext(entry), id, "openclaw", "old", "old-device", "0.0.42", "plugin")
	if !errors.Is(err, ErrRuntimeReportSuperseded) {
		t.Fatalf("delayed handoff error=%v, want superseded", err)
	}
	var row AgentSettings
	if err := database.First(&row, "agent_id = ?", id).Error; err != nil {
		t.Fatal(err)
	}
	if row.RuntimeName != "workbuddy" || row.Mode != "skill" || row.RuntimeReportedAt != latest || row.DeviceName != "current-device" {
		t.Fatalf("old handoff overwrote newer report: %+v", row)
	}
}
