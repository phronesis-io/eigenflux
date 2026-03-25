package notifyutil

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	pmNotifyKeyPrefix = "pm:notify:"
	pmNotifyTTL       = 7 * 24 * time.Hour
)

// WriteFriendRequestNotification writes a friend request notification to Redis
// for the recipient (toUID). Intended for fire-and-forget usage from the handler.
func WriteFriendRequestNotification(ctx context.Context, rdb *redis.Client, requestID, toUID int64, greeting string) error {
	key := fmt.Sprintf("%s%d", pmNotifyKeyPrefix, toUID)
	field := strconv.FormatInt(requestID, 10)

	content := "You have a new friend request"
	if greeting != "" {
		content += "\nGreeting: " + greeting
	}

	payload, err := json.Marshal(map[string]interface{}{
		"notification_id": field,
		"type":            "friend_request",
		"content":         content,
		"created_at":      time.Now().UnixMilli(),
	})
	if err != nil {
		return fmt.Errorf("marshal pm notification: %w", err)
	}

	pipe := rdb.TxPipeline()
	pipe.HSet(ctx, key, field, payload)
	pipe.Expire(ctx, key, pmNotifyTTL)
	_, err = pipe.Exec(ctx)
	return err
}

// WriteFriendResponseNotification writes a notification to the original requester
// when their friend request has been accepted or rejected.
// Uses negative request_id as the hash field to avoid collision with the
// friend_request notification that uses positive request_id.
// notifType should be "friend_accepted" or "friend_rejected".
func WriteFriendResponseNotification(ctx context.Context, rdb *redis.Client, requestID, toUID int64, notifType, reason string) error {
	key := fmt.Sprintf("%s%d", pmNotifyKeyPrefix, toUID)
	negID := -requestID
	field := strconv.FormatInt(negID, 10)

	content := "Your friend request has been accepted"
	if notifType == "friend_rejected" {
		content = "Your friend request has been declined"
	}
	if reason != "" {
		content += "\nReason: " + reason
	}

	payload, err := json.Marshal(map[string]interface{}{
		"notification_id": field,
		"type":            notifType,
		"content":         content,
		"created_at":      time.Now().UnixMilli(),
	})
	if err != nil {
		return fmt.Errorf("marshal pm notification: %w", err)
	}

	pipe := rdb.TxPipeline()
	pipe.HSet(ctx, key, field, payload)
	pipe.Expire(ctx, key, pmNotifyTTL)
	_, err = pipe.Exec(ctx)
	return err
}
