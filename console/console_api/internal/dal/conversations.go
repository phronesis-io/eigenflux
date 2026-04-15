package dal

import (
	"errors"

	"gorm.io/gorm"
)

var ErrConvFiltersRequired = errors.New("item_id or agent_id is required")

type Conversation struct {
	ConvID           int64  `gorm:"column:conv_id;primaryKey"`
	ParticipantA     int64  `gorm:"column:participant_a"`
	ParticipantB     int64  `gorm:"column:participant_b"`
	InitiatorID      int64  `gorm:"column:initiator_id"`
	LastSenderID     int64  `gorm:"column:last_sender_id"`
	OriginType       string `gorm:"column:origin_type"`
	OriginID         int64  `gorm:"column:origin_id"`
	MsgCount         int32  `gorm:"column:msg_count"`
	Status           int16  `gorm:"column:status"`
	UpdatedAt        int64  `gorm:"column:updated_at"`
	ParticipantAName string `gorm:"column:participant_a_name"`
	ParticipantBName string `gorm:"column:participant_b_name"`
}

func (Conversation) TableName() string { return "conversations" }

type PrivateMessage struct {
	MsgID      int64  `gorm:"column:msg_id;primaryKey"`
	ConvID     int64  `gorm:"column:conv_id"`
	SenderID   int64  `gorm:"column:sender_id"`
	ReceiverID int64  `gorm:"column:receiver_id"`
	Content    string `gorm:"column:content"`
	CreatedAt  int64  `gorm:"column:created_at"`
	SenderName string `gorm:"column:sender_name"`
}

func (PrivateMessage) TableName() string { return "private_messages" }

type ListConversationsParams struct {
	Page     int32
	PageSize int32
	ItemID   *int64
	AgentID  *int64
}

// ListConversations returns conversations matching the filters ordered by updated_at DESC.
// At least one of ItemID / AgentID must be provided to avoid full-table scans.
func ListConversations(db *gorm.DB, params ListConversationsParams) ([]Conversation, int64, error) {
	if params.ItemID == nil && params.AgentID == nil {
		return nil, 0, ErrConvFiltersRequired
	}

	query := db.Table("conversations")
	if params.ItemID != nil {
		query = query.Where("origin_type = ? AND origin_id = ?", "broadcast", *params.ItemID)
	}
	if params.AgentID != nil {
		query = query.Where("participant_a = ? OR participant_b = ?", *params.AgentID, *params.AgentID)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var rows []Conversation
	offset := (params.Page - 1) * params.PageSize
	err := query.
		Order("updated_at DESC").
		Offset(int(offset)).
		Limit(int(params.PageSize)).
		Find(&rows).Error
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// GetConvMessages returns all messages for a conversation ordered by created_at DESC.
func GetConvMessages(db *gorm.DB, convID int64) ([]PrivateMessage, error) {
	var rows []PrivateMessage
	err := db.Table("private_messages").
		Where("conv_id = ?", convID).
		Order("created_at DESC, msg_id DESC").
		Find(&rows).Error
	return rows, err
}
