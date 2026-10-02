package dal

import (
	"context"
	"os"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestMatchAgentsByNameLiteralAndBound(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN required")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	t.Cleanup(func() { sqlDB.Close() })
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	for _, sql := range []string{
		`CREATE TEMP TABLE agents(agent_id BIGINT,short_id TEXT,agent_name TEXT,agent_name_en TEXT)`,
		`INSERT INTO agents VALUES(1,'AbCdE','合同_%!','English NAME'),(2,'Other','合同x','')`,
	} {
		if err := tx.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		query string
		count int
	}{{"合同_%!", 1}, {"english name", 1}, {"AbCdE", 1}, {"abcde", 0}, {"AbCd", 0}, {"合同", 2}, {"%", 1}, {"_", 1}, {"!", 1}} {
		ids, more, err := MatchAgentsByName(context.Background(), tx, tc.query)
		if err != nil || more || len(ids) != tc.count {
			t.Fatalf("%q: %v %v %v", tc.query, ids, more, err)
		}
	}
	if err := tx.Exec(`INSERT INTO agents SELECT n,'id-'||n,'broad','' FROM generate_series(3,1003) n`).Error; err != nil {
		t.Fatal(err)
	}
	ids, more, err := MatchAgentsByName(context.Background(), tx, "broad")
	if err != nil || !more || len(ids) != 1000 {
		t.Fatalf("count=%d more=%v err=%v", len(ids), more, err)
	}
}
