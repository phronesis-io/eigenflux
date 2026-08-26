package activity

import (
	"context"
	"fmt"
	"strconv"
	"time"

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

func publishAsync(ctx context.Context, agentID int64, eventType, summary, detail string) {
	base := context.WithoutCancel(ctx)
	go func() {
		publishCtx, cancel := context.WithTimeout(base, 3*time.Second)
		defer cancel()
		publish(publishCtx, agentID, eventType, summary, detail)
	}()
}

// PublishFeedPull emits a feed_pull event asynchronously. itemCount is carried
// in detail so the consumer can sum delivered signals (today's items_scanned)
// and increment the all-time impression counter (signals_scanned).
func PublishFeedPull(ctx context.Context, agentID int64, itemCount int) {
	detail := fmt.Sprintf(`{"count":%d}`, itemCount)
	publishAsync(ctx, agentID, "feed_pull", fmt.Sprintf("Pulled feed, %d new signals", itemCount), detail)
}

// PublishBroadcast emits a broadcast event asynchronously.
func PublishBroadcast(ctx context.Context, agentID int64, itemID int64) {
	publishAsync(ctx, agentID, "broadcast", "Published 1 broadcast", "")
}

// PublishFeedback emits a feedback event asynchronously. count is the total
// items the agent gave feedback on; useful is the subset marked useful
// (score=2); kept is the subset worth reading (score>=1). All three are carried
// in detail so feedbacks_given, you_marked_useful and worth_reading can be
// summed independently.
func PublishFeedback(ctx context.Context, agentID int64, count, useful, kept int) {
	detail := fmt.Sprintf(`{"count":%d,"useful":%d,"kept":%d}`, count, useful, kept)
	publishAsync(ctx, agentID, "feedback", fmt.Sprintf("Gave feedback on %d broadcasts", count), detail)
}

// PublishMessageSent emits a message_sent event asynchronously.
func PublishMessageSent(ctx context.Context, agentID int64, receiverName string) {
	summary := "Sent a private message"
	if receiverName != "" {
		summary = fmt.Sprintf("Sent message to %s", receiverName)
	}
	publishAsync(ctx, agentID, "message_sent", summary, "")
}

// PublishReplyReceived emits a reply_received event asynchronously.
func PublishReplyReceived(ctx context.Context, agentID int64, senderName string) {
	summary := "Received a reply"
	if senderName != "" {
		summary = fmt.Sprintf("Received reply from %s", senderName)
	}
	publishAsync(ctx, agentID, "reply_received", summary, "")
}

// PublishMessageReceived emits a message_received event for the recipient of
// an ordinary private message. It is distinct from reply_received, which is
// retained for the legacy broadcast-reply metric.
func PublishMessageReceived(ctx context.Context, agentID int64, senderName string) {
	summary := "Received a private message"
	if senderName != "" {
		summary = fmt.Sprintf("Received message from %s", senderName)
	}
	go publish(ctx, agentID, "message_received", summary, "")
}

// PublishFriendRequestSent and PublishFriendRequestReceived record the two
// viewer-relative sides of a pending relationship request.
func PublishFriendRequestSent(ctx context.Context, agentID int64) {
	go publish(ctx, agentID, "friend_request_sent", "Sent a friend request", "")
}

func PublishFriendRequestReceived(ctx context.Context, agentID int64) {
	go publish(ctx, agentID, "friend_request_received", "Received a friend request", "")
}

// PublishProfileUpdate emits a profile_update event asynchronously, recorded
// when the agent refreshes its bio. Low-frequency (vs. feed_pull), so the
// console can pin the most recent one rather than let it scroll away.
func PublishProfileUpdate(ctx context.Context, agentID int64) {
	publishAsync(ctx, agentID, "profile_update", "Updated profile bio", "")
}

func PublishAgentJoined(ctx context.Context, agentID int64) {
	publishAsync(ctx, agentID, "agent_joined", "Joined the EigenFlux network", "")
}

func PublishAgentCardUpdate(ctx context.Context, agentID int64) {
	publishAsync(ctx, agentID, "agent_card_update", "Confirmed the Agent Card", "")
}

func PublishNetworkGoalUpdate(ctx context.Context, agentID int64) {
	publishAsync(ctx, agentID, "network_goal_update", "Updated the network activity goal", "")
}

func PublishIntentActionsUpdate(ctx context.Context, agentID int64, count int) {
	detail := fmt.Sprintf(`{"count":%d}`, count)
	publishAsync(ctx, agentID, "intent_actions_update", "Updated intents and actions", detail)
}

func PublishOnboardingCompleted(ctx context.Context, agentID int64) {
	publishAsync(ctx, agentID, "onboarding_completed", "Completed Console V2 onboarding", "")
}

// PublishFriendAdded emits a friend_added event asynchronously.
func PublishFriendAdded(ctx context.Context, agentID int64, friendName string) {
	summary := "Formed a new relation"
	if friendName != "" {
		summary = fmt.Sprintf("Formed relation with %s", friendName)
	}
	publishAsync(ctx, agentID, "friend_added", summary, "")
}
