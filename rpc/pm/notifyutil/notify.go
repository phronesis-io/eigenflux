package notifyutil

import (
	"context"
	"eigenflux_server/pkg/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	pmNotifyKeyPrefix = "pm:notify:"
	pmNotifyTTL       = 7 * 24 * time.Hour
)

type FriendResponseEvent struct {
	Type            string `json:"type"`
	FriendUID       string `json:"friend_uid"`
	PeerShortID     string `json:"peer_short_id"`
	PeerDisplayName string `json:"peer_display_name"`
}

func MarshalFriendResponseEvent(notifType string, friendUID int64, peerShortID, peerDisplayName string) (string, error) {
	payload, err := json.Marshal(FriendResponseEvent{
		Type: notifType, FriendUID: strconv.FormatInt(friendUID, 10),
		PeerShortID: peerShortID, PeerDisplayName: singleLine(peerDisplayName),
	})
	if err != nil {
		return "", fmt.Errorf("marshal friend response event: %w", err)
	}
	return string(payload), nil
}

// WriteFriendRequestNotification writes a friend request notification to Redis
// for the recipient (toUID). Intended for fire-and-forget usage from the handler.
func WriteFriendRequestNotification(ctx context.Context, rdb *redis.Client, requestID, toUID, friendUID int64, peerShortID, peerDisplayName, greeting string) error {
	key := fmt.Sprintf("%s%d", pmNotifyKeyPrefix, toUID)
	field := strconv.FormatInt(requestID, 10)

	peerDisplayName = singleLine(peerDisplayName)
	content := "You have a new friend request"
	if peerDisplayName != "" {
		content += " from " + peerDisplayName
	}
	if greeting != "" {
		content += "\nGreeting: " + greeting
	}

	payload, err := json.Marshal(map[string]interface{}{
		"notification_id":   field,
		"type":              "friend_request",
		"content":           content,
		"created_at":        time.Now().UnixMilli(),
		"friend_uid":        friendUID,
		"peer_short_id":     peerShortID,
		"peer_display_name": peerDisplayName,
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
func WriteFriendResponseNotification(ctx context.Context, rdb *redis.Client, requestID, toUID, friendUID int64, peerShortID, peerDisplayName, notifType, reason string) error {
	key := fmt.Sprintf("%s%d", pmNotifyKeyPrefix, toUID)
	negID := -requestID
	field := strconv.FormatInt(negID, 10)

	peerDisplayName = singleLine(peerDisplayName)
	content := "Your friend request has been accepted"
	if notifType == "friend_rejected" {
		content = "Your friend request has been declined"
	}
	if peerDisplayName != "" {
		content += " by " + peerDisplayName
	}
	if reason != "" {
		content += "\nReason: " + reason
	}

	payload, err := json.Marshal(map[string]interface{}{
		"notification_id":   field,
		"type":              notifType,
		"content":           content,
		"created_at":        time.Now().UnixMilli(),
		"friend_uid":        friendUID,
		"peer_short_id":     peerShortID,
		"peer_display_name": peerDisplayName,
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

func singleLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

// DeletePMNotifications removes friend-request notification entries by positive request ID.
func DeletePMNotifications(ctx context.Context, rdb *redis.Client, agentID int64, requestIDs ...int64) error {
	if rdb == nil || len(requestIDs) == 0 {
		return nil
	}

	key := fmt.Sprintf("%s%d", pmNotifyKeyPrefix, agentID)
	fields := make([]string, 0, len(requestIDs))
	for _, requestID := range requestIDs {
		fields = append(fields, strconv.FormatInt(requestID, 10))
	}
	return rdb.HDel(ctx, key, fields...).Err()
}
