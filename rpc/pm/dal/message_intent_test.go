package dal

import (
	"errors"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestReplyIntentRequiresInboundMessageInSameConversation(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE private_messages (msg_id INTEGER PRIMARY KEY, conv_id INTEGER, sender_id INTEGER, receiver_id INTEGER);
		INSERT INTO private_messages VALUES (10,100,2,1),(11,100,1,2),(12,200,2,1),(13,100,3,1),(14,100,2,3)`).Error; err != nil {
		t.Fatal(err)
	}
	kind := "reply"
	for _, tc := range []struct {
		id    int64
		valid bool
	}{{10, true}, {11, false}, {12, false}, {13, false}, {14, false}, {99, false}, {0, false}} {
		msg := &PrivateMessage{MsgID: 20, ConvID: 100, SenderID: 1, ReceiverID: 2, MessageKind: &kind, ReplyToMessageID: &tc.id}
		err := ValidateMessageIntent(db, msg)
		if tc.valid && err != nil {
			t.Fatalf("valid reply rejected: %v", err)
		}
		if !tc.valid && !errors.Is(err, ErrInvalidMessageIntent) {
			t.Fatalf("target %d not rejected: %v", tc.id, err)
		}
	}
	if err := ValidateMessageIntent(db, &PrivateMessage{}); err != nil {
		t.Fatalf("legacy intent must stay unknown: %v", err)
	}
	contact := "contact"
	if err := ValidateMessageIntent(db, &PrivateMessage{MessageKind: &contact}); err != nil {
		t.Fatal(err)
	}
}
