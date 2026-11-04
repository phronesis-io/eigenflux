package integrationprofile

import (
	"context"
	"fmt"
	"testing"

	"eigenflux_server/pkg/integrationidentity"
	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func contextStore(t *testing.T) *Store {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", uuid.NewString())), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = conn.Close() })
	if err := db.AutoMigrate(&State{}, &integrationidentity.App{}, &integrationidentity.Agent{}); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`CREATE TABLE integration_control(id INTEGER PRIMARY KEY,enabled BOOLEAN)`,
		`INSERT INTO integration_control VALUES(1,true)`,
		`INSERT INTO integration_apps(app_id,enabled) VALUES('partner',true)`,
		`INSERT INTO integration_agents(agent_id,app_id,enabled) VALUES(1,'partner',true)`,
	} {
		if err := db.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	return &Store{DB: db}
}

func summaryInput(revision int64, text string) Input {
	return Input{SourceRevision: revision, Summary: &text}
}

func TestStaleExtractionCannotWriteAfterNewContext(t *testing.T) {
	s := contextStore(t)
	ctx := context.Background()
	if _, err := s.Accept(ctx, 1, summaryInput(1, "first context")); err != nil {
		t.Fatal(err)
	}
	old, err := s.Claim(ctx, 1)
	if err != nil || old == nil {
		t.Fatal(old, err)
	}
	if _, err := s.Accept(ctx, 1, summaryInput(2, "new context")); err != nil {
		t.Fatal(err)
	}
	writes := 0
	writer := func(*gorm.DB, int64) (int64, error) { writes++; return 10, nil }
	if applied, err := s.Commit(ctx, old, writer); err != nil || applied || writes != 0 {
		t.Fatal(applied, err, writes)
	}
	if err := s.Fail(ctx, old); err != nil {
		t.Fatal(err)
	}
	job, err := s.Claim(ctx, 1)
	if err != nil || job == nil || job.Input.Summary != "new context" {
		t.Fatal(job, err)
	}
	if applied, err := s.Commit(ctx, job, writer); err != nil || !applied || writes != 1 {
		t.Fatal(applied, err, writes)
	}
	if applied, err := s.Commit(ctx, job, writer); err != nil || applied || writes != 1 {
		t.Fatal(applied, err, writes)
	}
}

func TestEqualContentKeepsLeaseAndAdvancesRevision(t *testing.T) {
	s := contextStore(t)
	ctx := context.Background()
	if _, err := s.Accept(ctx, 1, summaryInput(1, "context")); err != nil {
		t.Fatal(err)
	}
	job, err := s.Claim(ctx, 1)
	if err != nil || job == nil {
		t.Fatal(job, err)
	}
	state, err := s.Accept(ctx, 1, summaryInput(2, "context"))
	if err != nil || state.Generation != job.Generation || state.ClaimToken != job.ClaimToken {
		t.Fatal(state, err)
	}
	if next, err := s.Claim(ctx, 1); next != nil || err != nil {
		t.Fatal(next, err)
	}
	applied, err := s.Commit(ctx, job, func(_ *gorm.DB, revision int64) (int64, error) {
		if revision != 2 {
			t.Fatalf("writer received revision %d", revision)
		}
		return 7, nil
	})
	if err != nil || !applied {
		t.Fatal(applied, err)
	}
	state, err = s.Get(ctx, 1)
	if err != nil || state.AppliedMatchingVersion != 2 || state.ProfileVersion != 7 {
		t.Fatal(state, err)
	}
}

func TestDisabledApplicationPreventsAsyncWrite(t *testing.T) {
	s := contextStore(t)
	ctx := context.Background()
	if _, err := s.Accept(ctx, 1, summaryInput(1, "private context")); err != nil {
		t.Fatal(err)
	}
	job, err := s.Claim(ctx, 1)
	if err != nil || job == nil {
		t.Fatal(job, err)
	}
	if err := s.DB.Exec(`UPDATE integration_apps SET enabled=false`).Error; err != nil {
		t.Fatal(err)
	}
	applied, err := s.Commit(ctx, job, func(*gorm.DB, int64) (int64, error) { t.Fatal("disabled writer executed"); return 0, nil })
	if applied || err == nil {
		t.Fatal(applied, err)
	}
	if _, err := s.Accept(ctx, 1, summaryInput(2, "new")); err == nil {
		t.Fatal("disabled application accepted context")
	}
}
