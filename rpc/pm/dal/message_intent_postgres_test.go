package dal

import (
	"errors"
	"os"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestMessageIntentPostgresPersistsOnlyValidReply(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN is required for message intent persistence tests")
	}
	database, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	tx := database.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	for _, sql := range []string{
		`CREATE TEMP TABLE agents(agent_id bigint PRIMARY KEY,identity_state text) ON COMMIT DROP`,
		`INSERT INTO agents VALUES(1,'active'),(2,'active'),(3,'active')`,
		`CREATE TEMP TABLE private_messages(msg_id bigint PRIMARY KEY,conv_id bigint,sender_id bigint,receiver_id bigint,content text,is_read boolean,created_at bigint,sender_name text,receiver_name text,message_kind text,reply_to_message_id bigint) ON COMMIT DROP`,
		`INSERT INTO private_messages(msg_id,conv_id,sender_id,receiver_id) VALUES(10,100,2,1),(11,200,2,1)`,
	} {
		if err := tx.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	kind, target := "reply", int64(10)
	valid := &PrivateMessage{MsgID: 20, ConvID: 100, SenderID: 1, ReceiverID: 2, Content: "fixture reply", MessageKind: &kind, ReplyToMessageID: &target}
	if err := CreateMessage(tx, valid); err != nil {
		t.Fatal(err)
	}
	var stored PrivateMessage
	if err := tx.Where("msg_id=20").Take(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.MessageKind == nil || *stored.MessageKind != "reply" || stored.ReplyToMessageID == nil || *stored.ReplyToMessageID != 10 || stored.CreatedAt <= 0 {
		t.Fatalf("attribution not persisted: %+v", stored)
	}
	wrongTarget := int64(11)
	invalid := &PrivateMessage{MsgID: 21, ConvID: 100, SenderID: 1, ReceiverID: 2, Content: "must not persist", MessageKind: &kind, ReplyToMessageID: &wrongTarget}
	if err := CreateMessage(tx, invalid); !errors.Is(err, ErrInvalidMessageIntent) {
		t.Fatalf("cross-conversation reply accepted: %v", err)
	}
	var count int64
	if err := tx.Model(&PrivateMessage{}).Where("msg_id=21").Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("rejected reply was stored: %d %v", count, err)
	}
	if err := CreateMessage(tx, &PrivateMessage{MsgID: 22, ConvID: 100, SenderID: 1, ReceiverID: 2, Content: "legacy"}); err != nil {
		t.Fatal(err)
	}
	var unknown bool
	if err := tx.Raw(`SELECT message_kind IS NULL AND reply_to_message_id IS NULL FROM private_messages WHERE msg_id=22`).Scan(&unknown).Error; err != nil || !unknown {
		t.Fatalf("legacy send misclassified: %v", err)
	}
}
