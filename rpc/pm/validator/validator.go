package validator

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"
	"gorm.io/gorm"

	"eigenflux_server/rpc/pm/dal"
)

type Validator struct {
	db  *gorm.DB
	rdb *redis.Client
	sfg singleflight.Group
}

func NewValidator(db *gorm.DB, rdb *redis.Client) *Validator {
	return &Validator{
		db:  db,
		rdb: rdb,
	}
}

// ValidateItemOwnership validates that item_id exists and belongs to receiver_id
// Returns (ownerID, error)
func (v *Validator) ValidateItemOwnership(ctx context.Context, itemID, expectedOwnerID int64) error {
	cacheKey := fmt.Sprintf("pm:itemowner:%d", itemID)

	// Try cache first
	cached, err := v.rdb.Get(ctx, cacheKey).Result()
	if err == nil {
		if cached == "null" {
			return fmt.Errorf("item not found")
		}
		var ownerID int64
		if err := json.Unmarshal([]byte(cached), &ownerID); err == nil {
			if ownerID != expectedOwnerID {
				return fmt.Errorf("item does not belong to receiver")
			}
			return nil
		}
	}

	// Singleflight DB query
	result, err, _ := v.sfg.Do(cacheKey, func() (interface{}, error) {
		ownerID, dbErr := dal.GetItemOwner(v.db, itemID)
		if dbErr != nil {
			// Cache null result
			_ = v.rdb.Set(ctx, cacheKey, "null", 300*time.Second).Err()
			return int64(0), dbErr
		}

		// Cache result
		data, _ := json.Marshal(ownerID)
		_ = v.rdb.Set(ctx, cacheKey, data, 120*time.Minute).Err()
		return ownerID, nil
	})

	if err != nil {
		return fmt.Errorf("item not found")
	}

	ownerID := result.(int64)
	if ownerID != expectedOwnerID {
		return fmt.Errorf("item does not belong to receiver")
	}

	return nil
}

// ValidateNoReply checks if item has expected_response = 'no_reply'
func (v *Validator) ValidateNoReply(ctx context.Context, itemID int64) error {
	cacheKey := fmt.Sprintf("pm:itemresp:%d", itemID)

	// Try cache first
	cached, err := v.rdb.Get(ctx, cacheKey).Result()
	if err == nil {
		if cached == "no_reply" {
			return fmt.Errorf("this item does not accept replies")
		}
		return nil
	}

	// Singleflight DB query
	result, err, _ := v.sfg.Do(cacheKey, func() (interface{}, error) {
		resp, dbErr := dal.GetItemExpectedResponse(v.db, itemID)
		if dbErr != nil {
			return "", dbErr
		}
		val := resp
		if val == "" {
			val = "_empty"
		}
		_ = v.rdb.Set(ctx, cacheKey, val, 300*time.Second).Err()
		return resp, nil
	})

	if err != nil {
		return fmt.Errorf("failed to check expected_response: %w", err)
	}

	if result.(string) == "no_reply" {
		return fmt.Errorf("this item does not accept replies")
	}

	return nil
}

type ConvInfo struct {
	ParticipantA int64
	ParticipantB int64
	Status       int16
}

// ValidateConvMembership validates that sender is a participant in the conversation
// Returns (receiverID, error)
func (v *Validator) ValidateConvMembership(ctx context.Context, convID, senderID int64) (int64, error) {
	cacheKey := fmt.Sprintf("pm:conv:%d", convID)

	// Try cache first
	cached, err := v.rdb.Get(ctx, cacheKey).Result()
	if err == nil {
		var info ConvInfo
		if err := json.Unmarshal([]byte(cached), &info); err == nil {
			return v.checkMembership(&info, senderID)
		}
	}

	// Singleflight DB query
	result, err, _ := v.sfg.Do(cacheKey, func() (interface{}, error) {
		conv, dbErr := dal.GetConversationByID(v.db, convID)
		if dbErr != nil {
			return nil, dbErr
		}

		info := &ConvInfo{
			ParticipantA: conv.ParticipantA,
			ParticipantB: conv.ParticipantB,
			Status:       conv.Status,
		}

		// Cache result
		data, _ := json.Marshal(info)
		_ = v.rdb.Set(ctx, cacheKey, data, 300*time.Second).Err()
		return info, nil
	})

	if err != nil {
		return 0, fmt.Errorf("conversation not found")
	}

	info := result.(*ConvInfo)
	return v.checkMembership(info, senderID)
}

func (v *Validator) checkMembership(info *ConvInfo, senderID int64) (int64, error) {
	if info.Status == 1 {
		return 0, fmt.Errorf("conversation is blocked")
	}
	if info.Status == 2 {
		return 0, fmt.Errorf("conversation is locked")
	}

	if senderID == info.ParticipantA {
		return info.ParticipantB, nil
	} else if senderID == info.ParticipantB {
		return info.ParticipantA, nil
	}

	return 0, fmt.Errorf("sender is not a participant in this conversation")
}

// GetOrCreateConvID checks if conversation exists via Redis mapping
func (v *Validator) GetOrCreateConvID(ctx context.Context, participantA, participantB, originID int64) (int64, bool, error) {
	mapKey := fmt.Sprintf("pm:convmap:%d:%d:%d", participantA, participantB, originID)

	// Check Redis mapping
	cached, err := v.rdb.Get(ctx, mapKey).Result()
	if err == nil {
		var convID int64
		if json.Unmarshal([]byte(cached), &convID) == nil {
			return convID, true, nil
		}
	}

	// Check DB
	conv, err := dal.GetConversationByParticipants(v.db, participantA, participantB, originID)
	if err == nil {
		// Found in DB, cache it
		data, _ := json.Marshal(conv.ConvID)
		_ = v.rdb.Set(ctx, mapKey, data, 7*24*time.Hour).Err()
		return conv.ConvID, true, nil
	}

	// Not found
	return 0, false, nil
}

// CacheConvMapping caches the conversation mapping
func (v *Validator) CacheConvMapping(ctx context.Context, participantA, participantB, originID, convID int64) error {
	mapKey := fmt.Sprintf("pm:convmap:%d:%d:%d", participantA, participantB, originID)
	data, _ := json.Marshal(convID)
	return v.rdb.Set(ctx, mapKey, data, 7*24*time.Hour).Err()
}

// InvalidateConvCache removes the cached conversation info so subsequent checks hit DB
func (v *Validator) InvalidateConvCache(ctx context.Context, convID int64) {
	cacheKey := fmt.Sprintf("pm:conv:%d", convID)
	v.rdb.Del(ctx, cacheKey)
}
