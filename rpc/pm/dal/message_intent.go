package dal

import (
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrInvalidMessageIntent = errors.New("invalid message intent or reply target")

// ValidateMessageIntent only accepts explicit intent. An existing conversation
// is not sufficient evidence that a message replies to another participant.
// The caller holds the participant locks and writes the message in this transaction.
func ValidateMessageIntent(tx *gorm.DB, msg *PrivateMessage) error {
	if msg.MessageKind == nil {
		if msg.ReplyToMessageID != nil {
			return ErrInvalidMessageIntent
		}
		return nil
	}
	switch *msg.MessageKind {
	case "contact":
		if msg.ReplyToMessageID != nil {
			return ErrInvalidMessageIntent
		}
		return nil
	case "reply":
		if msg.ReplyToMessageID == nil || *msg.ReplyToMessageID <= 0 || *msg.ReplyToMessageID == msg.MsgID {
			return ErrInvalidMessageIntent
		}
		var target PrivateMessage
		err := tx.Select("msg_id").Clauses(clause.Locking{Strength: "KEY SHARE"}).
			Where("msg_id = ? AND conv_id = ? AND sender_id = ? AND receiver_id = ?",
				*msg.ReplyToMessageID, msg.ConvID, msg.ReceiverID, msg.SenderID).Take(&target).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrInvalidMessageIntent
		}
		return err
	default:
		return ErrInvalidMessageIntent
	}
}
