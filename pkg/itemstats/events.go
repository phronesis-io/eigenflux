package itemstats

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"eigenflux_server/pkg/mq"
)

const (
	StreamName = "stream:item:stats"
	GroupName  = "cg:item:stats"

	EventTypeConsumed = "consumed"
	EventTypeFeedback = "feedback"
)

// Feedback is one accepted score report. Optional telemetry never creates a
// follow-up event; ClientKey deduplicates durable score effects per Agent.
type Feedback struct {
	AgentID, ItemID                                            int64
	Score                                                      int
	ImpressionID, ClientKey, ImpressionSource, ContractVersion string
	Surfaced                                                   *bool
}

type Event struct {
	EventType                                    string
	AgentID                                      int64
	ItemID                                       int64
	Score                                        int
	ImpressionID                                 string
	ClientKey, ImpressionSource, ContractVersion string
	Surfaced                                     *bool
}

var ErrNilPublisher = errors.New("item stats publisher is nil")

func PublishConsumed(ctx context.Context, agentID, itemID int64) (string, error) {
	if mq.RDB == nil {
		return "", ErrNilPublisher
	}
	return mq.Publish(ctx, StreamName, map[string]interface{}{
		"event_type": EventTypeConsumed,
		"agent_id":   strconv.FormatInt(agentID, 10),
		"item_id":    strconv.FormatInt(itemID, 10),
	})
}

func PublishFeedback(ctx context.Context, feedback Feedback) (string, error) {
	if mq.RDB == nil {
		return "", ErrNilPublisher
	}
	values := map[string]interface{}{
		"event_type": EventTypeFeedback,
		"agent_id":   strconv.FormatInt(feedback.AgentID, 10),
		"item_id":    strconv.FormatInt(feedback.ItemID, 10),
		"score":      strconv.Itoa(feedback.Score),
	}
	for key, value := range map[string]string{
		"impression_id": feedback.ImpressionID, "client_key": feedback.ClientKey,
		"impression_source": feedback.ImpressionSource, "contract_version": feedback.ContractVersion,
	} {
		if value != "" {
			values[key] = value
		}
	}
	if feedback.Surfaced != nil {
		values["surfaced"] = strconv.FormatBool(*feedback.Surfaced)
	}
	return mq.Publish(ctx, StreamName, values)
}

func ParseEvent(values map[string]interface{}) (Event, error) {
	eventType, ok := values["event_type"].(string)
	if !ok || eventType == "" {
		return Event{}, fmt.Errorf("missing event_type")
	}

	agentIDStr, ok := values["agent_id"].(string)
	if !ok || agentIDStr == "" {
		return Event{}, fmt.Errorf("missing agent_id")
	}
	agentID, err := strconv.ParseInt(agentIDStr, 10, 64)
	if err != nil {
		return Event{}, fmt.Errorf("invalid agent_id %q: %w", agentIDStr, err)
	}

	itemIDStr, ok := values["item_id"].(string)
	if !ok || itemIDStr == "" {
		return Event{}, fmt.Errorf("missing item_id")
	}
	itemID, err := strconv.ParseInt(itemIDStr, 10, 64)
	if err != nil {
		return Event{}, fmt.Errorf("invalid item_id %q: %w", itemIDStr, err)
	}

	event := Event{
		EventType: eventType,
		AgentID:   agentID,
		ItemID:    itemID,
	}

	switch eventType {
	case EventTypeConsumed:
		return event, nil
	case EventTypeFeedback:
		scoreStr, ok := values["score"].(string)
		if !ok || scoreStr == "" {
			return Event{}, fmt.Errorf("missing score")
		}
		score, err := strconv.Atoi(scoreStr)
		if err != nil || score < -1 || score > 2 {
			return Event{}, fmt.Errorf("invalid score %q", scoreStr)
		}
		event.Score = score
		if impressionID, ok := values["impression_id"].(string); ok && impressionID != "" {
			event.ImpressionID = impressionID
		}
		event.ClientKey, _ = values["client_key"].(string)
		event.ImpressionSource, _ = values["impression_source"].(string)
		event.ContractVersion, _ = values["contract_version"].(string)
		// An unreadable optional declaration stays unknown; the consumer drops
		// unparseable messages, which would discard a valid score.
		if value, ok := values["surfaced"].(string); ok {
			if surfaced, err := strconv.ParseBool(value); err == nil {
				event.Surfaced = &surfaced
			}
		}
		return event, nil
	default:
		return Event{}, fmt.Errorf("unsupported event_type %q", eventType)
	}
}
