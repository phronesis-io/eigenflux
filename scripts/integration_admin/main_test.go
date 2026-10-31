package main

import (
	"bytes"
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"eigenflux_server/pkg/integrationidentity"
	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestPostgresOperatorDryRunCredentialAndPolicy(t *testing.T) {
	dsn := os.Getenv("INTEGRATION_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("isolated loopback PostgreSQL required")
	}
	u, err := url.Parse(dsn)
	if err != nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") {
		t.Fatal("explicit loopback PostgreSQL required")
	}
	open := func(dsn string) *gorm.DB {
		database, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		if err != nil {
			t.Fatal(err)
		}
		return database
	}
	admin := open(dsn)
	conn, _ := admin.DB()
	defer conn.Close()
	schema := "operator_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	defer admin.Exec("DROP SCHEMA " + schema + " CASCADE")
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	database := open(u.String())
	connection, _ := database.DB()
	defer connection.Close()
	if err := database.Exec(`CREATE TABLE agents(agent_id BIGINT PRIMARY KEY)`).Error; err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"000125_enterprise_integrations.sql", "000126_integration_contexts.sql", "000127_integration_feed_batches.sql", "000128_integration_request_observation.sql"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		up, _, _ := strings.Cut(string(raw), "-- +goose Down")
		if err := database.Exec(up).Error; err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PG_DSN", u.String())
	ctx := context.Background()
	call := func(args ...string) (string, error) {
		var output bytes.Buffer
		err := run(ctx, args, &output)
		if strings.Contains(output.String(), "efi_") || strings.Contains(output.String(), "credential_hash") {
			t.Fatal("credential exposed in operator output")
		}
		return output.String(), err
	}
	args := []string{"--action=create-app", "--app-id=partner", "--name=Partner", "--max-agents=8", "--max-feed-batches-per-day=50", "--actor=operator", "--reason=contract"}
	if output, err := call(args...); err != nil || !strings.Contains(output, "dry-run") {
		t.Fatal(output, err)
	}
	var count int64
	if err := database.Table("integration_apps").Count(&count).Error; err != nil || count != 0 {
		t.Fatal("dry-run changed data", count, err)
	}
	credential := filepath.Join(t.TempDir(), "application.credential")
	if _, err := call(append(args, "--apply", "--credential-out="+credential)...); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(credential)
	if err != nil {
		t.Fatal(err)
	}
	secret := strings.TrimSpace(string(raw))
	store := &integrationidentity.Store{DB: database}
	app, err := store.Authenticate(ctx, secret)
	if err != nil || app.Enabled || app.MaxAgents != 8 {
		t.Fatal(app, err)
	}
	if _, err := call("--action=set-policy", "--app-id=partner", "--min-poll-seconds=7200", "--actor=operator", "--reason=contract", "--apply"); err != nil {
		t.Fatal(err)
	}
	policy, err := store.AppPolicy(ctx, "partner")
	if err != nil || policy.MaxAgents != 8 || policy.MaxFeedBatchesPerDay != 50 || policy.MinPollIntervalSeconds != 7200 {
		t.Fatal(policy, err)
	}
	if _, err := call("--action=set-enabled", "--scope=global", "--enabled=true", "--actor=operator", "--reason=contract"); err != nil {
		t.Fatal(err)
	}
	var enabled bool
	if err := database.Table("integration_control").Select("enabled").Where("id=1").Scan(&enabled).Error; err != nil || enabled {
		t.Fatal("dry-run enabled global switch", enabled, err)
	}
	rotate := []string{"--action=rotate-credential", "--app-id=partner", "--actor=operator", "--reason=contract", "--apply"}
	if _, err := call(append(rotate, "--credential-out="+credential)...); err == nil {
		t.Fatal("existing credential file overwritten")
	}
	if _, err := store.Authenticate(ctx, secret); err != nil {
		t.Fatal("failed file preflight rotated credential", err)
	}
	nextCredential := filepath.Join(t.TempDir(), "rotated.credential")
	if _, err := call(append(rotate, "--credential-out="+nextCredential)...); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Authenticate(ctx, secret); err == nil {
		t.Fatal("old credential retained access")
	}
	if output, err := call("--app-id=partner"); err != nil || !strings.Contains(output, "max_feed_batches_per_day") {
		t.Fatal(output, err)
	}
	if output, err := call("--action=audit", "--app-id=partner"); err != nil || !strings.Contains(output, "credential_rotated") {
		t.Fatal(output, err)
	}
}
