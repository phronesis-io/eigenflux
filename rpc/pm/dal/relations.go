package dal

import (
	"errors"
	"time"

	"gorm.io/gorm"
)

const (
	RelTypeFriend = 1
	RelTypeBlock  = 2

	RequestStatusPending    = 0
	RequestStatusAccepted   = 1
	RequestStatusRejected   = 2
	RequestStatusCancelled  = 3
	RequestStatusUnfriended = 4
)

type UserRelation struct {
	ID        int64  `gorm:"column:id;primaryKey"`
	FromUID   int64  `gorm:"column:from_uid;not null"`
	ToUID     int64  `gorm:"column:to_uid;not null"`
	RelType   int16  `gorm:"column:rel_type;not null"`
	CreatedAt int64  `gorm:"column:created_at;not null"`
	Remark    string `gorm:"column:remark;not null;default:''"`
}

func (UserRelation) TableName() string { return "user_relations" }

type FriendRequest struct {
	ID        int64  `gorm:"column:id;primaryKey"`
	FromUID   int64  `gorm:"column:from_uid;not null"`
	ToUID     int64  `gorm:"column:to_uid;not null"`
	Status    int16  `gorm:"column:status;not null;default:0"`
	Greeting  string `gorm:"column:greeting;not null;default:''"`
	CreatedAt int64  `gorm:"column:created_at;not null"`
	UpdatedAt int64  `gorm:"column:updated_at;not null"`
}

func (FriendRequest) TableName() string { return "friend_requests" }

type Friend struct {
	AgentID     int64
	AgentName   string
	FriendSince int64
	Remark      string
}

// CreateFriendRequest creates a new friend request with the given snowflake ID
func CreateFriendRequest(db *gorm.DB, id, fromUID, toUID int64, greeting string) (int64, error) {
	now := time.Now().UnixMilli()
	req := &FriendRequest{
		ID:        id,
		FromUID:   fromUID,
		ToUID:     toUID,
		Status:    RequestStatusPending,
		Greeting:  greeting,
		CreatedAt: now,
		UpdatedAt: now,
	}
	err := db.Create(req).Error
	return req.ID, err
}

// GetFriendRequest retrieves a friend request by ID
func GetFriendRequest(db *gorm.DB, requestID int64) (*FriendRequest, error) {
	var req FriendRequest
	err := db.Where("id = ?", requestID).First(&req).Error
	return &req, err
}

// GetPendingRequestBetween checks if there's a pending request from fromUID to toUID
func GetPendingRequestBetween(db *gorm.DB, fromUID, toUID int64) (*FriendRequest, error) {
	var req FriendRequest
	err := db.Where("from_uid = ? AND to_uid = ? AND status = ?", fromUID, toUID, RequestStatusPending).First(&req).Error
	return &req, err
}

// UpdateRequestStatus updates the status of a friend request
func UpdateRequestStatus(db *gorm.DB, requestID int64, status int16) error {
	return db.Model(&FriendRequest{}).
		Where("id = ?", requestID).
		Updates(map[string]interface{}{
			"status":     status,
			"updated_at": time.Now().UnixMilli(),
		}).Error
}

// CreateFriendRelation creates 2 symmetric friend relation rows in a transaction.
// remarkByA is the remark set by uidA for uidB, remarkByB is the remark set by uidB for uidA.
func CreateFriendRelation(tx *gorm.DB, uidA, uidB int64, remarkByA, remarkByB string) error {
	now := time.Now().UnixMilli()
	relations := []UserRelation{
		{FromUID: uidA, ToUID: uidB, RelType: RelTypeFriend, CreatedAt: now, Remark: remarkByA},
		{FromUID: uidB, ToUID: uidA, RelType: RelTypeFriend, CreatedAt: now, Remark: remarkByB},
	}
	return tx.Create(&relations).Error
}

// DeleteFriendRelation deletes 2 symmetric friend relation rows in a transaction
func DeleteFriendRelation(tx *gorm.DB, uidA, uidB int64) error {
	result := tx.Where("((from_uid = ? AND to_uid = ?) OR (from_uid = ? AND to_uid = ?)) AND rel_type = ?",
		uidA, uidB, uidB, uidA, RelTypeFriend).Delete(&UserRelation{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 2 {
		return errors.New("expected to delete 2 friend relation rows")
	}
	return nil
}

// CreateBlockRelation creates a block relation row with optional remark
func CreateBlockRelation(tx *gorm.DB, fromUID, toUID int64, remark string) error {
	now := time.Now().UnixMilli()
	rel := &UserRelation{
		FromUID:   fromUID,
		ToUID:     toUID,
		RelType:   RelTypeBlock,
		CreatedAt: now,
		Remark:    remark,
	}
	return tx.Create(rel).Error
}

// DeleteBlockRelation deletes a block relation row
func DeleteBlockRelation(tx *gorm.DB, fromUID, toUID int64) error {
	result := tx.Where("from_uid = ? AND to_uid = ? AND rel_type = ?", fromUID, toUID, RelTypeBlock).
		Delete(&UserRelation{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return errors.New("block relation not found")
	}
	return nil
}

// IsFriend checks if two users are friends
func IsFriend(db *gorm.DB, uidA, uidB int64) (bool, error) {
	var count int64
	err := db.Model(&UserRelation{}).
		Where("from_uid = ? AND to_uid = ? AND rel_type = ?", uidA, uidB, RelTypeFriend).
		Count(&count).Error
	return count > 0, err
}

// IsBlocked checks if fromUID has blocked toUID
func IsBlocked(db *gorm.DB, fromUID, toUID int64) (bool, error) {
	var count int64
	err := db.Model(&UserRelation{}).
		Where("from_uid = ? AND to_uid = ? AND rel_type = ?", fromUID, toUID, RelTypeBlock).
		Count(&count).Error
	return count > 0, err
}

// ListFriendRequests retrieves friend requests with pagination
func ListFriendRequests(db *gorm.DB, agentID int64, direction string, cursor, limit int) ([]*FriendRequest, error) {
	var requests []*FriendRequest
	query := db.Where("status = ?", RequestStatusPending)

	if direction == "incoming" {
		query = query.Where("to_uid = ?", agentID)
	} else {
		query = query.Where("from_uid = ?", agentID)
	}

	if cursor > 0 {
		query = query.Where("created_at < ?", cursor)
	}

	err := query.Order("created_at DESC").Limit(limit).Find(&requests).Error
	return requests, err
}

// ListFriends retrieves friends with names and pagination
func ListFriends(db *gorm.DB, agentID int64, cursor, limit int) ([]*Friend, error) {
	// First query: get friend relations
	var relations []UserRelation
	query := db.Where("from_uid = ? AND rel_type = ?", agentID, RelTypeFriend)
	if cursor > 0 {
		query = query.Where("created_at < ?", cursor)
	}
	err := query.Order("created_at DESC").Limit(limit).Find(&relations).Error
	if err != nil {
		return nil, err
	}

	if len(relations) == 0 {
		return []*Friend{}, nil
	}

	// Second query: batch get agent names
	friendIDs := make([]int64, len(relations))
	for i, rel := range relations {
		friendIDs[i] = rel.ToUID
	}
	nameMap, err := BatchGetAgentNames(db, friendIDs)
	if err != nil {
		return nil, err
	}

	// Combine results
	friends := make([]*Friend, len(relations))
	remarkMap := make(map[int64]string, len(relations))
	for _, rel := range relations {
		remarkMap[rel.ToUID] = rel.Remark
	}
	for i, rel := range relations {
		friends[i] = &Friend{
			AgentID:     rel.ToUID,
			AgentName:   nameMap[rel.ToUID],
			FriendSince: rel.CreatedAt,
			Remark:      remarkMap[rel.ToUID],
		}
	}

	return friends, nil
}

// UpdateFriendRemark updates the remark for a friend relation.
func UpdateFriendRemark(db *gorm.DB, agentID, friendUID int64, remark string) error {
	result := db.Model(&UserRelation{}).
		Where("from_uid = ? AND to_uid = ? AND rel_type = ?", agentID, friendUID, RelTypeFriend).
		Update("remark", remark)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return errors.New("friend relation not found")
	}
	return nil
}

