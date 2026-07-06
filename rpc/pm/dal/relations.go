package dal

import (
	"errors"
	"fmt"
	"hash/fnv"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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

// GetRemarksByPeer returns the requester's remarks keyed by peer uid for the
// given peers; only non-empty remarks are included.
func GetRemarksByPeer(db *gorm.DB, fromUID int64, toUIDs []int64) (map[int64]string, error) {
	remarks := make(map[int64]string, len(toUIDs))
	if len(toUIDs) == 0 {
		return remarks, nil
	}
	var rels []UserRelation
	if err := db.Where("from_uid = ? AND to_uid IN ?", fromUID, toUIDs).Find(&rels).Error; err != nil {
		return nil, err
	}
	for _, r := range rels {
		if r.Remark != "" {
			remarks[r.ToUID] = r.Remark
		}
	}
	return remarks, nil
}

type FriendRequest struct {
	ID        int64  `gorm:"column:id;primaryKey"`
	FromUID   int64  `gorm:"column:from_uid;not null"`
	ToUID     int64  `gorm:"column:to_uid;not null"`
	Status    int16  `gorm:"column:status;not null;default:0"`
	Greeting  string `gorm:"column:greeting;not null;default:''"`
	Remark    string `gorm:"column:remark;not null;default:''"`
	CreatedAt int64  `gorm:"column:created_at;not null"`
	UpdatedAt int64  `gorm:"column:updated_at;not null"`
}

func (FriendRequest) TableName() string { return "friend_requests" }

type Friend struct {
	RelationID  int64
	AgentID     int64
	AgentName   string
	Bio         string
	FriendSince int64
	Remark      string
}

// CreateFriendRequest creates a new friend request with the given snowflake ID.
func CreateFriendRequest(db *gorm.DB, id, fromUID, toUID int64, greeting, remark string) (int64, error) {
	now := time.Now().UnixMilli()
	req := &FriendRequest{
		ID:        id,
		FromUID:   fromUID,
		ToUID:     toUID,
		Status:    RequestStatusPending,
		Greeting:  greeting,
		Remark:    remark,
		CreatedAt: now,
		UpdatedAt: now,
	}
	err := db.Create(req).Error
	return req.ID, err
}

// GetFriendRequest retrieves a friend request by ID.
func GetFriendRequest(db *gorm.DB, requestID int64) (*FriendRequest, error) {
	var req FriendRequest
	err := db.Where("id = ?", requestID).First(&req).Error
	return &req, err
}

// GetFriendRequestForUpdate retrieves a friend request by ID with a row lock.
func GetFriendRequestForUpdate(tx *gorm.DB, requestID int64) (*FriendRequest, error) {
	var req FriendRequest
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", requestID).
		First(&req).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &req, err
}

// GetFriendRequestBetweenForUpdate loads the logical request row between two users with a row lock.
func GetFriendRequestBetweenForUpdate(tx *gorm.DB, fromUID, toUID int64) (*FriendRequest, error) {
	var req FriendRequest
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("from_uid = ? AND to_uid = ?", fromUID, toUID).
		First(&req).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &req, err
}

// UpdateRequestStatus updates the status of a friend request.
func UpdateRequestStatus(db *gorm.DB, requestID int64, status int16) error {
	return db.Model(&FriendRequest{}).
		Where("id = ?", requestID).
		Updates(map[string]interface{}{
			"status":     status,
			"updated_at": time.Now().UnixMilli(),
		}).Error
}

// UpdateRequestStatusIfPending updates the status only when the current status is pending.
func UpdateRequestStatusIfPending(tx *gorm.DB, requestID int64, status int16) (bool, error) {
	result := tx.Model(&FriendRequest{}).
		Where("id = ? AND status = ?", requestID, RequestStatusPending).
		Updates(map[string]interface{}{
			"status":     status,
			"updated_at": time.Now().UnixMilli(),
		})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

// ResetFriendRequest reuses an existing logical row with a newly generated request ID.
func ResetFriendRequest(tx *gorm.DB, existingID, newID int64, greeting, remark string) error {
	now := time.Now().UnixMilli()
	return tx.Model(&FriendRequest{}).
		Where("id = ?", existingID).
		Updates(map[string]interface{}{
			"id":         newID,
			"status":     RequestStatusPending,
			"greeting":   greeting,
			"remark":     remark,
			"created_at": now,
			"updated_at": now,
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

// DeleteFriendRelation deletes 2 symmetric friend relation rows in a transaction.
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

// CreateBlockRelation creates a block relation row with optional remark.
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

// DeleteBlockRelation deletes a block relation row.
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

// IsFriend checks if two users are friends.
func IsFriend(db *gorm.DB, uidA, uidB int64) (bool, error) {
	var count int64
	err := db.Model(&UserRelation{}).
		Where("from_uid = ? AND to_uid = ? AND rel_type = ?", uidA, uidB, RelTypeFriend).
		Count(&count).Error
	return count > 0, err
}

// FriendSet returns the subset of peerIDs that are currently friends of agentID
// (user_relations rel_type=1). Relation rows are symmetric double-writes, so a
// single-direction lookup (from_uid = agentID) suffices. Used to derive the
// broadcast_comment / non_friend split for a page of conversations in one query.
func FriendSet(db *gorm.DB, agentID int64, peerIDs []int64) (map[int64]bool, error) {
	set := make(map[int64]bool, len(peerIDs))
	if len(peerIDs) == 0 {
		return set, nil
	}
	var ids []int64
	err := db.Model(&UserRelation{}).
		Where("from_uid = ? AND rel_type = ? AND to_uid IN ?", agentID, RelTypeFriend, peerIDs).
		Pluck("to_uid", &ids).Error
	if err != nil {
		return set, err
	}
	for _, id := range ids {
		set[id] = true
	}
	return set, nil
}

// Category derives the conversation bucket from origin_type and the caller's
// current friendship with the peer:
//   - "friend"            : origin_type = 'friend'
//   - "broadcast_comment" : origin_type = 'broadcast' AND currently friends
//   - "non_friend"        : origin_type = 'broadcast' AND not friends
func Category(originType string, isFriend bool) string {
	if originType == "friend" {
		return "friend"
	}
	if isFriend {
		return "broadcast_comment"
	}
	return "non_friend"
}

// IsBlocked checks if fromUID has blocked toUID.
func IsBlocked(db *gorm.DB, fromUID, toUID int64) (bool, error) {
	var count int64
	err := db.Model(&UserRelation{}).
		Where("from_uid = ? AND to_uid = ? AND rel_type = ?", fromUID, toUID, RelTypeBlock).
		Count(&count).Error
	return count > 0, err
}

// LockRelationPair serializes relation state transitions for a user pair.
func LockRelationPair(tx *gorm.DB, uidA, uidB int64) error {
	if uidA > uidB {
		uidA, uidB = uidB, uidA
	}

	hasher := fnv.New64a()
	_, _ = hasher.Write([]byte(fmt.Sprintf("%d:%d", uidA, uidB)))
	key := int64(hasher.Sum64())

	return tx.Exec("SELECT pg_advisory_xact_lock(?)", key).Error
}

// ListFriendRequests retrieves friend requests with pagination.
// Returns (requests, hasMore, error). Uses LIMIT+1 probe to determine has_more.
func ListFriendRequests(db *gorm.DB, agentID int64, direction string, cursor int64, limit int) ([]*FriendRequest, bool, error) {
	var requests []*FriendRequest
	query := db.Where("status = ?", RequestStatusPending)

	if direction == "incoming" {
		query = query.Where("to_uid = ?", agentID)
	} else {
		query = query.Where("from_uid = ?", agentID)
	}

	if cursor > 0 {
		query = query.Where("id < ?", cursor)
	}

	err := query.Order("id DESC").Limit(limit + 1).Find(&requests).Error
	if err != nil {
		return nil, false, err
	}
	if len(requests) > limit {
		return requests[:limit], true, nil
	}
	return requests, false, nil
}

// officialFriendIDs returns the subset of an agent's friends that are ops-flagged
// official accounts (agents.is_official) — normally just the singleton new-user
// guide. Used to pin the official account to the top of the relations list.
func officialFriendIDs(db *gorm.DB, agentID int64) ([]int64, error) {
	var ids []int64
	err := db.Model(&UserRelation{}).
		Joins("JOIN agents a ON a.agent_id = user_relations.to_uid").
		Where("user_relations.from_uid = ? AND user_relations.rel_type = ? AND a.is_official",
			agentID, RelTypeFriend).
		Pluck("user_relations.to_uid", &ids).Error
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// ListFriends retrieves friends with names and pagination. Official accounts are
// pinned to the very top of the first page (cursor == 0) and excluded from the
// normal id-DESC body on every page, so the official account always leads the
// list and never appears twice across pages.
func ListFriends(db *gorm.DB, agentID int64, cursor int64, limit int) ([]*Friend, error) {
	official, err := officialFriendIDs(db, agentID)
	if err != nil {
		return nil, err
	}

	var relations []UserRelation
	query := db.Where("from_uid = ? AND rel_type = ?", agentID, RelTypeFriend)
	if len(official) > 0 {
		query = query.Where("to_uid NOT IN ?", official)
	}
	if cursor > 0 {
		query = query.Where("id < ?", cursor)
	}
	if err := query.Order("id DESC").Limit(limit).Find(&relations).Error; err != nil {
		return nil, err
	}

	// First page: prepend the official account(s) ahead of the id-DESC body.
	if cursor == 0 && len(official) > 0 {
		var pinned []UserRelation
		if err := db.Where("from_uid = ? AND rel_type = ? AND to_uid IN ?",
			agentID, RelTypeFriend, official).
			Order("id DESC").Find(&pinned).Error; err != nil {
			return nil, err
		}
		relations = append(pinned, relations...)
	}

	if len(relations) == 0 {
		return []*Friend{}, nil
	}

	friendIDs := make([]int64, len(relations))
	for i, rel := range relations {
		friendIDs[i] = rel.ToUID
	}
	profileMap, err := BatchGetAgentProfiles(db, friendIDs)
	if err != nil {
		return nil, err
	}

	friends := make([]*Friend, len(relations))
	for i, rel := range relations {
		p := profileMap[rel.ToUID]
		friends[i] = &Friend{
			RelationID:  rel.ID,
			AgentID:     rel.ToUID,
			AgentName:   p.AgentName,
			Bio:         p.Bio,
			FriendSince: rel.CreatedAt,
			Remark:      rel.Remark,
		}
	}

	return friends, nil
}

// CountFriends returns the exact number of friends for an agent.
func CountFriends(db *gorm.DB, agentID int64) (int64, error) {
	var n int64
	err := db.Model(&UserRelation{}).
		Where("from_uid = ? AND rel_type = ?", agentID, RelTypeFriend).
		Count(&n).Error
	return n, err
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
