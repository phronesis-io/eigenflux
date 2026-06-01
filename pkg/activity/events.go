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

// publish emits an activity event. detail is an optional JSON string stored in
// the agent_activity_log.detail column, used to carry quantities (e.g. item
// counts) that the consumer aggregates — event rows alone only support COUNT(*).
func publish(ctx context.Context, agentID int64, eventType, summary, detail string) {
	values := map[string]interface{}{
		"agent_id":   strconv.FormatInt(agentID, 10),
		"event_type": eventType,
		"summary":    summary,
	}
	if detail != "" {
		values["detail"] = detail
	}
	_, err := mq.Publish(ctx, StreamName, values)
	if err != nil {
		logger.Default().Warn("failed to publish activity event",
			"event_type", eventType, "agent_id", agentID, "err", err)
	}
}

// PublishFeedPull emits a feed_pull event asynchronously. itemCount is carried
// in detail so the consumer can sum delivered signals (today's items_scanned)
// and increment the all-time impression counter (signals_scanned).
func PublishFeedPull(ctx context.Context, agentID int64, itemCount int) {
	detail := fmt.Sprintf(`{"count":%d}`, itemCount)
	go publish(ctx, agentID, "feed_pull", fmt.Sprintf("Pulled feed, %d new signals", itemCount), detail)
}

// PublishBroadcast emits a broadcast event asynchronously.
func PublishBroadcast(ctx context.Context, agentID int64, itemID int64) {
	go publish(ctx, agentID, "broadcast", "Published 1 broadcast", "")
}

// PublishFeedback emits a feedback event asynchronously. count is the total
// items the agent gave feedback on; useful is the subset marked useful
// (score=2). Both are carried in detail so today's feedbacks_given and
// you_marked_useful can be summed independently.
func PublishFeedback(ctx context.Context, agentID int64, count, useful int) {
	detail := fmt.Sprintf(`{"count":%d,"useful":%d}`, count, useful)
	go publish(ctx, agentID, "feedback", fmt.Sprintf("Gave feedback on %d broadcasts", count), detail)
}

// PublishMessageSent emits a message_sent event asynchronously.
func PublishMessageSent(ctx context.Context, agentID int64, receiverName string) {
	go publish(ctx, agentID, "message_sent", fmt.Sprintf("Sent message to %s", receiverName), "")
}

// PublishReplyReceived emits a reply_received event asynchronously.
func PublishReplyReceived(ctx context.Context, agentID int64, senderName string) {
	go publish(ctx, agentID, "reply_received", fmt.Sprintf("Received reply from %s", senderName), "")
}

// PublishFriendAdded emits a friend_added event asynchronously.
func PublishFriendAdded(ctx context.Context, agentID int64, friendName string) {
	go publish(ctx, agentID, "friend_added", fmt.Sprintf("Formed relation with %s", friendName), "")
}
