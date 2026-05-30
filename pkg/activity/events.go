package activity

import (
	"context"
	"fmt"
	"strconv"

	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/mq"
)

const (
	StreamName = "stream:agent:activity"
	GroupName  = "cg:agent:activity"
)

func publish(ctx context.Context, agentID int64, eventType, summary string) {
	_, err := mq.Publish(ctx, StreamName, map[string]interface{}{
		"agent_id":   strconv.FormatInt(agentID, 10),
		"event_type": eventType,
		"summary":    summary,
	})
	if err != nil {
		logger.Default().Warn("failed to publish activity event",
			"event_type", eventType, "agent_id", agentID, "err", err)
	}
}

// PublishFeedPull emits a feed_pull event asynchronously.
func PublishFeedPull(ctx context.Context, agentID int64, itemCount int) {
	go publish(ctx, agentID, "feed_pull", fmt.Sprintf("Pulled feed, %d new signals", itemCount))
}

// PublishBroadcast emits a broadcast event asynchronously.
func PublishBroadcast(ctx context.Context, agentID int64, itemID int64) {
	go publish(ctx, agentID, "broadcast", "Published 1 broadcast")
}

// PublishFeedback emits a feedback event asynchronously.
func PublishFeedback(ctx context.Context, agentID int64, count int) {
	go publish(ctx, agentID, "feedback", fmt.Sprintf("Gave feedback on %d broadcasts", count))
}

// PublishMessageSent emits a message_sent event asynchronously.
func PublishMessageSent(ctx context.Context, agentID int64, receiverName string) {
	go publish(ctx, agentID, "message_sent", fmt.Sprintf("Sent message to %s", receiverName))
}

// PublishReplyReceived emits a reply_received event asynchronously.
func PublishReplyReceived(ctx context.Context, agentID int64, senderName string) {
	go publish(ctx, agentID, "reply_received", fmt.Sprintf("Received reply from %s", senderName))
}

// PublishFriendAdded emits a friend_added event asynchronously.
func PublishFriendAdded(ctx context.Context, agentID int64, friendName string) {
	go publish(ctx, agentID, "friend_added", fmt.Sprintf("Formed relation with %s", friendName))
}
