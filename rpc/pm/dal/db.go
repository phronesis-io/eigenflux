package dal

import (
	"eigenflux_server/pkg/logger"
	"errors"
	"time"

	"gorm.io/gorm"
)

type Conversation struct {
	ConvID           int64  `gorm:"column:conv_id;primaryKey"`
	ParticipantA     int64  `gorm:"column:participant_a;not null"`
	ParticipantB     int64  `gorm:"column:participant_b;not null"`
	InitiatorID      int64  `gorm:"column:initiator_id;not null"`
	LastSenderID     int64  `gorm:"column:last_sender_id;not null"`
	OriginType       string `gorm:"column:origin_type;type:varchar(20)"`
	OriginID         int64  `gorm:"column:origin_id"`
	MsgCount         int    `gorm:"column:msg_count;not null;default:0"`
	Status           int16  `gorm:"column:status;type:smallint;not null;default:0"`
	UpdatedAt        int64  `gorm:"column:updated_at;not null"`
	ParticipantAName string `gorm:"column:participant_a_name;type:varchar(100);not null;default:''"`
	ParticipantBName string `gorm:"column:participant_b_name;type:varchar(100);not null;default:''"`
}

func (Conversation) TableName() string { return "conversations" }

type PrivateMessage struct {
	MsgID        int64  `gorm:"column:msg_id;primaryKey"`
	ConvID       int64  `gorm:"column:conv_id;not null"`
	SenderID     int64  `gorm:"column:sender_id;not null"`
	ReceiverID   int64  `gorm:"column:receiver_id;not null"`
	Content      string `gorm:"column:content;type:text;not null"`
	IsRead       bool   `gorm:"column:is_read;not null;default:false"`
	CreatedAt    int64  `gorm:"column:created_at;not null"`
	SenderName   string `gorm:"column:sender_name;type:varchar(100);not null;default:''"`
	ReceiverName string `gorm:"column:receiver_name;type:varchar(100);not null;default:''"`
}

func (PrivateMessage) TableName() string { return "private_messages" }

// CreateConversation creates a new conversation
func CreateConversation(db *gorm.DB, conv *Conversation) error {
	conv.UpdatedAt = time.Now().UnixMilli()
	return db.Create(conv).Error
}

// GetConversationByID retrieves a conversation by conv_id
func GetConversationByID(db *gorm.DB, convID int64) (*Conversation, error) {
	var conv Conversation
	err := db.Where("conv_id = ?", convID).First(&conv).Error
	return &conv, err
}

// GetConversationByParticipants retrieves a conversation by participant pair and origin_id
func GetConversationByParticipants(db *gorm.DB, participantA, participantB, originID int64) (*Conversation, error) {
	var conv Conversation
	err := db.Where("participant_a = ? AND participant_b = ? AND origin_id = ?", participantA, participantB, originID).First(&conv).Error
	return &conv, err
}

// CreateMessage creates a new private message
func CreateMessage(db *gorm.DB, msg *PrivateMessage) error {
	msg.CreatedAt = time.Now().UnixMilli()
	return db.Create(msg).Error
}

// UpdateConversationAfterMessage updates conversation metadata after a message is sent
func UpdateConversationAfterMessage(db *gorm.DB, convID, senderID int64) error {
	return db.Model(&Conversation{}).
		Where("conv_id = ?", convID).
		Updates(map[string]interface{}{
			"last_sender_id": senderID,
			"msg_count":      gorm.Expr("msg_count + 1"),
			"updated_at":     time.Now().UnixMilli(),
		}).Error
}

// FetchUnreadMessages retrieves unread messages for an agent
func FetchUnreadMessages(db *gorm.DB, agentID, cursor int64, limit int) ([]*PrivateMessage, error) {
	var messages []*PrivateMessage
	query := db.Where("receiver_id = ? AND is_read = ?", agentID, false)
	if cursor > 0 {
		query = query.Where("msg_id > ?", cursor)
	}
	err := query.Order("msg_id ASC").Limit(limit).Find(&messages).Error
	return messages, err
}

// FetchRecentReadMessages returns up to `limit` messages involving agentID
// that are NOT currently eligible for FetchUnreadMessages, i.e.:
//   (receiver_id = agentID AND is_read = TRUE)  // already-read received
//   OR sender_id = agentID                       // anything agent sent
// Ordered by msg_id DESC. Used by FetchPMHistory for recovery-on-reconnect.
func FetchRecentReadMessages(db *gorm.DB, agentID int64, limit int) ([]*PrivateMessage, error) {
	var messages []*PrivateMessage
	err := db.Where(
		"(receiver_id = ? AND is_read = ?) OR sender_id = ?",
		agentID, true, agentID,
	).Order("msg_id DESC").Limit(limit).Find(&messages).Error
	return messages, err
}

// MarkMessagesAsRead marks messages as read
func MarkMessagesAsRead(db *gorm.DB, msgIDs []int64) error {
	if len(msgIDs) == 0 {
		return nil
	}
	return db.Model(&PrivateMessage{}).
		Where("msg_id IN ?", msgIDs).
		Update("is_read", true).Error
}

// ListConversations retrieves ice-broken conversations (msg_count >= 2) for an agent.
// Uses UNION ALL on the two indexed columns to avoid OR-based sequential scan.
func ListConversations(db *gorm.DB, agentID, cursor int64, limit int) ([]*Conversation, error) {
	var convs []*Conversation
	var err error

	if cursor > 0 {
		err = db.Raw(
			`SELECT * FROM (
				(SELECT * FROM conversations WHERE participant_a = ? AND status = 0 AND msg_count >= 2 AND updated_at < ? ORDER BY updated_at DESC LIMIT ?)
				UNION ALL
				(SELECT * FROM conversations WHERE participant_b = ? AND status = 0 AND msg_count >= 2 AND updated_at < ? ORDER BY updated_at DESC LIMIT ?)
			) AS c ORDER BY updated_at DESC LIMIT ?`,
			agentID, cursor, limit,
			agentID, cursor, limit,
			limit,
		).Scan(&convs).Error
	} else {
		err = db.Raw(
			`SELECT * FROM (
				(SELECT * FROM conversations WHERE participant_a = ? AND status = 0 AND msg_count >= 2 ORDER BY updated_at DESC LIMIT ?)
				UNION ALL
				(SELECT * FROM conversations WHERE participant_b = ? AND status = 0 AND msg_count >= 2 ORDER BY updated_at DESC LIMIT ?)
			) AS c ORDER BY updated_at DESC LIMIT ?`,
			agentID, limit,
			agentID, limit,
			limit,
		).Scan(&convs).Error
	}

	return convs, err
}

// GetConvMessages retrieves messages for a conversation with cursor pagination (older messages)
func GetConvMessages(db *gorm.DB, convID, cursor int64, limit int) ([]*PrivateMessage, error) {
	var msgs []*PrivateMessage
	query := db.Where("conv_id = ?", convID)
	if cursor > 0 {
		query = query.Where("msg_id < ?", cursor)
	}
	err := query.Order("msg_id DESC").Limit(limit).Find(&msgs).Error
	return msgs, err
}

// GetItemOwner retrieves the author_agent_id for an item
func GetItemOwner(db *gorm.DB, itemID int64) (int64, error) {
	var result struct {
		AuthorAgentID int64
	}
	err := db.Table("raw_items").
		Select("author_agent_id").
		Where("item_id = ?", itemID).
		First(&result).Error
	if err != nil {
		return 0, err
	}
	return result.AuthorAgentID, nil
}

// GetItemExpectedResponse retrieves the expected_response for an item
func GetItemExpectedResponse(db *gorm.DB, itemID int64) (string, error) {
	var result struct {
		ExpectedResponse string
	}
	err := db.Table("processed_items").
		Select("COALESCE(expected_response, '') as expected_response").
		Where("item_id = ?", itemID).
		First(&result).Error
	if err != nil {
		return "", err
	}
	return result.ExpectedResponse, nil
}

// CloseConversation sets conversation status to closed (status=2)
func CloseConversation(db *gorm.DB, convID int64) error {
	result := db.Model(Conversation{}).
		Where("conv_id = ? AND origin_id > 0", convID).
		Update("status", 2)

	if result.Error != nil {
		return result.Error
	}

	// 检查是否有行被更新
	if result.RowsAffected == 0 {
		// 可能是 convID 不存在，或者该会话的 origin_id 等于 0
		logger.Default().Warn("CloseConversation: no rows affected (not found or origin_id=0)", "convID", convID)
		return errors.New("conversation not found or not item-originated")
	}

	return nil
}

// BatchGetAgentNames returns a map of agent_id → agent_name for the given IDs
func BatchGetAgentNames(db *gorm.DB, agentIDs []int64) (map[int64]string, error) {
	if len(agentIDs) == 0 {
		return map[int64]string{}, nil
	}
	var results []struct {
		AgentID   int64  `gorm:"column:agent_id"`
		AgentName string `gorm:"column:agent_name"`
	}
	err := db.Table("agents").Select("agent_id, agent_name").Where("agent_id IN ?", agentIDs).Find(&results).Error
	if err != nil {
		return nil, err
	}
	nameMap := make(map[int64]string, len(results))
	for _, r := range results {
		nameMap[r.AgentID] = r.AgentName
	}
	return nameMap, nil
}
