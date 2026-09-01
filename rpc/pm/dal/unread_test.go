package dal

import (
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestUnreadCountsExcludeSelfLoopMessages(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE conversations (conv_id INTEGER PRIMARY KEY, participant_a INTEGER NOT NULL, participant_b INTEGER NOT NULL, origin_type TEXT NOT NULL)`,
		`CREATE TABLE private_messages (msg_id INTEGER PRIMARY KEY, conv_id INTEGER NOT NULL, sender_id INTEGER NOT NULL, receiver_id INTEGER NOT NULL, is_read BOOLEAN NOT NULL)`,
		`CREATE TABLE user_relations (from_uid INTEGER NOT NULL, to_uid INTEGER NOT NULL, rel_type INTEGER NOT NULL)`,
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Exec(`INSERT INTO conversations (conv_id, participant_a, participant_b, origin_type) VALUES (1, 10, 20, 'broadcast'), (2, 10, 10, 'broadcast')`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO private_messages (msg_id, conv_id, sender_id, receiver_id, is_read) VALUES (1, 1, 20, 10, false), (2, 2, 10, 10, false)`).Error; err != nil {
		t.Fatal(err)
	}

	total, err := CountUnreadTotal(db, 10)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("expected only the external unread message, got %d", total)
	}
	comment, nonFriend, friend, err := CountUnreadByOrigin(db, 10)
	if err != nil {
		t.Fatal(err)
	}
	if comment != 0 || nonFriend != 1 || friend != 0 {
		t.Fatalf("unexpected unread breakdown: comment=%d non_friend=%d friend=%d", comment, nonFriend, friend)
	}
}
