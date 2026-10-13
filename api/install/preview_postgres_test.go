package install

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"eigenflux_server/pkg/db"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// The handler flow runs against a connection-scoped temporary table; no
// persisted installation rows are created or changed by this regression.
func TestPostgresPreviewLandingFlow(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN is required for preview landing persistence")
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
	stmt := &gorm.Statement{DB: gdb}
	if err := stmt.Parse(&Token{}); err != nil {
		t.Fatal(err)
	}
	var columns []string
	for _, field := range stmt.Schema.Fields {
		columnType := "text"
		if field.DataType == "int" {
			columnType = "bigint"
		}
		columns = append(columns, `"`+field.DBName+`" `+columnType)
	}
	if err := gdb.Exec("CREATE TEMP TABLE install_tokens (" + strings.Join(columns, ",") + ")").Error; err != nil {
		t.Fatal(err)
	}
	previous := db.DB
	db.DB = gdb
	t.Cleanup(func() { db.DB = previous })
	h := server.New()
	h.POST("/api/v1/install/token", mintRef)
	h.GET("/r/:ref", serveRef)
	mint := func(ua string) (int, string) {
		t.Helper()
		resp := ut.PerformRequest(h.Engine, http.MethodPost, "/api/v1/install/token",
			&ut.Body{Body: strings.NewReader(`{"utm_source":"youtube"}`), Len: len(`{"utm_source":"youtube"}`)},
			ut.Header{Key: "User-Agent", Value: ua}).Result()
		var body struct {
			Data struct {
				Ref string `json:"ref"`
			} `json:"data"`
		}
		if err := json.Unmarshal(resp.Body(), &body); err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode(), body.Data.Ref
	}
	for _, ua := range []string{"LarkUrl", "redditbot/1.0", "Bytespider"} {
		status, ref := mint(ua)
		if status != http.StatusForbidden || ref != "" {
			t.Fatalf("preview minted: %s status=%d ref=%s", ua, status, ref)
		}
	}
	for _, ua := range []string{"Mozilla/5.0 MicroMessenger/8.0", "Mozilla/5.0 QQ/9.0", "Mozilla/5.0 BiliApp/9.0", "Mozilla/5.0 Xiaohongshu/8.0"} {
		status, ref := mint(ua)
		if status != http.StatusOK || !ValidTokenFormat(ref) {
			t.Fatalf("real visit failed: %s status=%d ref=%s", ua, status, ref)
		}
		resp := ut.PerformRequest(h.Engine, http.MethodGet, "/r/"+ref, nil, ut.Header{Key: "User-Agent", Value: "LarkUrl"}).Result()
		if resp.StatusCode() != http.StatusOK {
			t.Fatalf("preview document status=%d", resp.StatusCode())
		}
		var token Token
		if err := gdb.First(&token, "token = ?", ref).Error; err != nil {
			t.Fatal(err)
		}
		if token.FetchedAt != 0 {
			t.Fatal("preview stamped fetch")
		}
		resp = ut.PerformRequest(h.Engine, http.MethodGet, "/r/"+ref, nil, ut.Header{Key: "User-Agent", Value: "curl/8.7.1"}).Result()
		if resp.StatusCode() != http.StatusOK {
			t.Fatalf("installer document status=%d", resp.StatusCode())
		}
		if err := gdb.First(&token, "token = ?", ref).Error; err != nil {
			t.Fatal(err)
		}
		if token.FetchedAt == 0 {
			t.Fatal("installer fetch was excluded")
		}
	}
	var count int64
	if err := gdb.Model(&Token{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 4 {
		t.Fatalf("landing rows=%d, want only four real visits", count)
	}
}
