package install

import (
	"os"
	"strconv"
	"testing"
	"time"

	"eigenflux_server/pkg/db"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestPostgresShortIDInviteAttribution(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN is required for PostgreSQL invite attribution semantics")
	}
	gdb, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	if err := gdb.Exec(`CREATE TEMP TABLE agents (
		agent_id bigint PRIMARY KEY, short_id varchar(5), email text NOT NULL,
		created_at bigint NOT NULL, acquisition_channel text NOT NULL DEFAULT '',
		invited_by_code text NOT NULL DEFAULT '', inviter_agent_id bigint NOT NULL DEFAULT 0,
		invited_at bigint NOT NULL DEFAULT 0
	) ON COMMIT PRESERVE ROWS`).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec(`CREATE TEMP TABLE invite_codes (
		code text PRIMARY KEY, kind text NOT NULL, agent_id bigint NOT NULL,
		name text NOT NULL DEFAULT '', note text NOT NULL DEFAULT '', created_at bigint NOT NULL,
		revoked_at bigint NULL
	) ON COMMIT PRESERVE ROWS`).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	if err := gdb.Exec(`INSERT INTO agents(agent_id, short_id, email, created_at)
		VALUES (100, 'AbCdE', 'inviter@test.com', ?),
		       (200, 'FgHiJ', 'invitee@test.com', ?)`, now-1000, now+1).Error; err != nil {
		t.Fatal(err)
	}
	previous := db.DB
	db.DB = gdb
	t.Cleanup(func() { db.DB = previous })

	attributeReportedAgent(&Token{
		InviteCode: "AbCdE", Channel: "user", CreatedAt: now,
	}, map[string]any{
		"agent_id": strconv.FormatInt(200, 10), "email": "invitee@test.com",
	})

	var row struct {
		Code      string `gorm:"column:invited_by_code"`
		InviterID int64  `gorm:"column:inviter_agent_id"`
		Channel   string `gorm:"column:acquisition_channel"`
	}
	if err := gdb.Raw(`SELECT invited_by_code, inviter_agent_id, acquisition_channel
		FROM agents WHERE agent_id=200`).Scan(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Code != "AbCdE" || row.InviterID != 100 || row.Channel != "user" {
		t.Fatalf("short-ID attribution=%+v, want AbCdE/100/user", row)
	}
}
