package install

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/mq"
	"github.com/alicebob/miniredis/v2"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestIOSInstallStatus(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN is required for install status persistence")
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
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := gdb.Exec(`CREATE TEMP TABLE install_tokens (
		token text PRIMARY KEY, status text NOT NULL, reported_at bigint DEFAULT 0,
		report_count bigint DEFAULT 0
	)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec("INSERT INTO install_tokens (token, status) VALUES (?, ?)", "EF-ios00001", StatusPending).Error; err != nil {
		t.Fatal(err)
	}

	cache := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: cache.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	oldDB, oldRedis := db.DB, mq.RDB
	db.DB, mq.RDB = gdb, rdb
	t.Cleanup(func() { db.DB, mq.RDB = oldDB, oldRedis })

	ref, key := "EF-ios00001", strings.Repeat("a", 64)
	digest := sha256.Sum256([]byte(key))
	if err := rememberStatusKey(context.Background(), rdb, ref, hex.EncodeToString(digest[:])); err != nil {
		t.Fatal(err)
	}

	h := server.New()
	h.POST("/api/v1/install/status", installStatus)
	fetch := func(key string, wantCode int, wantState string) {
		t.Helper()
		payload := `{"entries":[{"ref":"EF-ios00001","status_key":"` + key + `"}]}`
		res := ut.PerformRequest(h.Engine, http.MethodPost, "/api/v1/install/status",
			&ut.Body{Body: strings.NewReader(payload), Len: len(payload)}).Result()
		if res.StatusCode() != wantCode {
			t.Fatalf("status code = %d, want %d, body %s", res.StatusCode(), wantCode, res.Body())
		}
		if wantCode == http.StatusOK {
			var response struct{ Data map[string]string }
			if err := json.Unmarshal(res.Body(), &response); err != nil {
				t.Fatal(err)
			}
			if got := response.Data[ref]; got != wantState {
				t.Fatalf("status = %q, want %q", got, wantState)
			}
		}
	}
	fetch("", http.StatusBadRequest, "")
	fetch(strings.Repeat("b", 64), http.StatusOK, "unavailable")
	fetch(key, http.StatusOK, StatusPending)
	if converted, _, err := ReportInstall(gdb, ref); err != nil || !converted {
		t.Fatalf("ReportInstall converted=%v err=%v", converted, err)
	}
	fetch(key, http.StatusOK, StatusInstalled)
	cache.FastForward(statusKeyLifetime)
	fetch(key, http.StatusOK, "unavailable")
}
