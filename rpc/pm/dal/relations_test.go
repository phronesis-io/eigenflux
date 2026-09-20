package dal

import (
	"errors"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newRelationsTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	// Mirrors migrations/000009_add_user_relations.sql, including uq_relation.
	if err := db.Exec(`CREATE TABLE user_relations (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		from_uid INTEGER NOT NULL,
		to_uid INTEGER NOT NULL,
		rel_type INTEGER NOT NULL,
		remark TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL,
		CONSTRAINT uq_relation UNIQUE (from_uid, to_uid, rel_type))`).Error; err != nil {
		t.Fatal(err)
	}
	return db
}

func countRelations(t *testing.T, db *gorm.DB, fromUID, toUID int64, relType int16) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&UserRelation{}).
		Where("from_uid = ? AND to_uid = ? AND rel_type = ?", fromUID, toUID, relType).
		Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func TestDeleteFriendRelation_NotFriends(t *testing.T) {
	db := newRelationsTestDB(t)

	if err := DeleteFriendRelation(db, 10, 20); !errors.Is(err, ErrNotFriends) {
		t.Fatalf("expected ErrNotFriends, got %v", err)
	}
	if err := DeleteFriendRelation(db, 10, 10); !errors.Is(err, ErrNotFriends) {
		t.Fatalf("expected ErrNotFriends for self target, got %v", err)
	}
}

func TestDeleteFriendRelation_RemovesBothRows(t *testing.T) {
	db := newRelationsTestDB(t)
	if err := CreateFriendRelation(db, 10, 20, "b", "a"); err != nil {
		t.Fatal(err)
	}

	if err := DeleteFriendRelation(db, 20, 10); err != nil {
		t.Fatalf("expected friend relation delete to succeed, got %v", err)
	}
	if n := countRelations(t, db, 10, 20, RelTypeFriend) + countRelations(t, db, 20, 10, RelTypeFriend); n != 0 {
		t.Fatalf("expected both friend rows deleted, %d remain", n)
	}
	if err := DeleteFriendRelation(db, 10, 20); !errors.Is(err, ErrNotFriends) {
		t.Fatalf("expected ErrNotFriends after deletion, got %v", err)
	}
}

func TestCreateBlockRelation_AlreadyBlocked(t *testing.T) {
	db := newRelationsTestDB(t)
	if err := CreateBlockRelation(db, 10, 20, "spam"); err != nil {
		t.Fatal(err)
	}

	if err := CreateBlockRelation(db, 10, 20, "again"); !errors.Is(err, ErrAlreadyBlocked) {
		t.Fatalf("expected ErrAlreadyBlocked, got %v", err)
	}
	if n := countRelations(t, db, 10, 20, RelTypeBlock); n != 1 {
		t.Fatalf("expected a single block row, got %d", n)
	}
	var rel UserRelation
	if err := db.Where("from_uid = ? AND to_uid = ? AND rel_type = ?", 10, 20, RelTypeBlock).First(&rel).Error; err != nil {
		t.Fatal(err)
	}
	if rel.Remark != "spam" {
		t.Fatalf("expected the original remark to be kept, got %q", rel.Remark)
	}
	// The reverse direction is a distinct relation and is not a conflict.
	if err := CreateBlockRelation(db, 20, 10, ""); err != nil {
		t.Fatalf("expected reverse block to succeed, got %v", err)
	}
}

func TestDeleteBlockRelation_NotBlocked(t *testing.T) {
	db := newRelationsTestDB(t)

	if err := DeleteBlockRelation(db, 10, 20); !errors.Is(err, ErrNotBlocked) {
		t.Fatalf("expected ErrNotBlocked, got %v", err)
	}
	if err := CreateBlockRelation(db, 10, 20, ""); err != nil {
		t.Fatal(err)
	}
	if err := DeleteBlockRelation(db, 20, 10); !errors.Is(err, ErrNotBlocked) {
		t.Fatalf("expected ErrNotBlocked for the reverse direction, got %v", err)
	}
	if err := DeleteBlockRelation(db, 10, 20); err != nil {
		t.Fatalf("expected unblock to succeed, got %v", err)
	}
	if err := DeleteBlockRelation(db, 10, 20); !errors.Is(err, ErrNotBlocked) {
		t.Fatalf("expected ErrNotBlocked after deletion, got %v", err)
	}
}
