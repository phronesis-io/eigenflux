package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	feedrpc "eigenflux_server/kitex_gen/eigenflux/feed"
	"eigenflux_server/kitex_gen/eigenflux/feed/feedservice"
	"eigenflux_server/pkg/integrationfeed"
	"eigenflux_server/pkg/integrationidentity"
	"github.com/cloudwego/hertz/pkg/app"
)

func (s *Service) pullFeed(ctx context.Context, c *app.RequestContext) {
	bound, cancel := context.WithTimeout(ctx, 95*time.Second)
	defer cancel()
	agent, err := s.business(bound, c)
	if err != nil {
		fail(c, err)
		return
	}
	var request integrationfeed.Request
	if err := body(c, &request); err != nil {
		fail(c, err)
		return
	}
	if s.Feed == nil {
		fail(c, &integrationidentity.Error{Status: 503, Code: "INTEGRATION_FEED_SOURCE_UNAVAILABLE"})
		return
	}
	store := integrationfeed.Store{DB: s.Store.DB}
	batch, admitted, err := store.Begin(bound, agent.AgentID, request)
	if err != nil {
		fail(c, err)
		return
	}
	if admitted {
		limit := request.Limit
		if limit == 0 {
			limit = 20
		}
		sourceCtx, sourceCancel := context.WithTimeout(bound, 85*time.Second)
		generated, generateErr := s.Feed(sourceCtx, agent.AgentID, batch.MatchingVersion, limit)
		sourceCancel()
		if generateErr != nil || generated.MatchingVersion != batch.MatchingVersion {
			cleanup, done := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer done()
			_ = store.Unknown(cleanup, batch)
			fail(c, &integrationidentity.Error{Status: 503, Code: "FEED_GENERATION_UNKNOWN"})
			return
		}
		applied, commitErr := store.Complete(bound, batch, generated.Payload, generated.ItemCount)
		if commitErr != nil {
			fail(c, commitErr)
			return
		}
		if !applied {
			fail(c, &integrationidentity.Error{Status: 409, Code: "FEED_GENERATION_UNKNOWN"})
			return
		}
	}
	// This also rechecks ownership and current switches on cache/retry hits
	// and after a potentially long upstream call.
	if _, err := s.business(bound, c); err != nil {
		fail(c, err)
		return
	}
	current, err := store.Latest(bound, agent.AgentID)
	if err != nil {
		fail(c, err)
		return
	}
	if current.Sequence != batch.Sequence || current.BatchID != batch.BatchID {
		fail(c, &integrationidentity.Error{Status: 409, Code: "FEED_SEQUENCE_STALE"})
		return
	}
	feedReply(c, current)
}

func (s *Service) latestFeed(ctx context.Context, c *app.RequestContext) {
	bound, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	agent, err := s.business(bound, c)
	if err != nil {
		fail(c, err)
		return
	}
	store := integrationfeed.Store{DB: s.Store.DB}
	batch, err := store.Latest(bound, agent.AgentID)
	if err != nil {
		fail(c, err)
		return
	}
	feedReply(c, batch)
}

func feedReply(c *app.RequestContext, batch integrationfeed.Batch) {
	data := map[string]any{"batch": batch, "feed": map[string]any{"schema_version": "integration.feed.v1", "items": []any{}}}
	if batch.Status == "ready" {
		var payload map[string]json.RawMessage
		if err := json.Unmarshal([]byte(batch.Payload), &payload); err != nil {
			fail(c, &integrationidentity.Error{Status: 503, Code: "INTEGRATION_FEED_PAYLOAD_UNAVAILABLE"})
			return
		}
		data["feed"] = payload
	}
	c.JSON(200, map[string]any{"code": 0, "msg": "success", "data": data})
}

// RPCFeedSource reuses delivery/disclosure/ranking behavior, but emits a data
// contract only. No output_contract, behavior instructions or notifications.
func RPCFeedSource(client feedservice.Client) integrationfeed.Generator {
	return func(ctx context.Context, agentID, version int64, limit int) (integrationfeed.Generated, error) {
		var output integrationfeed.Generated
		action := "refresh"
		size := int32(limit)
		response, err := client.FetchFeed(ctx, &feedrpc.FetchFeedReq{AgentId: agentID, Action: &action, Limit: &size, ManagedMatchingVersion: &version})
		if err != nil || response == nil || response.BaseResp == nil || response.BaseResp.Code != 0 || response.ManagedMatchingVersion == nil || *response.ManagedMatchingVersion != version || len(response.Items) > limit {
			return output, errors.New("enterprise Feed source failed")
		}
		items := make([]map[string]any, 0, len(response.Items))
		for _, item := range response.Items {
			if item == nil {
				return output, errors.New("invalid enterprise Feed item")
			}
			entry := map[string]any{"item_id": strconv.FormatInt(item.ItemId, 10), "broadcast_type": item.BroadcastType, "updated_at": item.UpdatedAt}
			if item.Summary != nil {
				entry["summary"] = *item.Summary
			}
			if item.CreatedAt != nil {
				entry["created_at"] = *item.CreatedAt
			}
			if item.AuthorAgentId != nil {
				entry["author_agent_id"] = strconv.FormatInt(*item.AuthorAgentId, 10)
			}
			if item.DisplayName != nil {
				entry["display_name"] = *item.DisplayName
			}
			if item.RawUrl != nil {
				entry["url"] = *item.RawUrl
			}
			if item.RawContent != nil {
				entry["raw_content"] = *item.RawContent
			}
			if item.RawContentTruncated != nil {
				entry["raw_content_truncated"] = *item.RawContentTruncated
			}
			if item.Domains != nil {
				entry["domains"] = item.Domains
			}
			if item.Keywords != nil {
				entry["keywords"] = item.Keywords
			}
			if item.ExpireTime != nil {
				entry["expire_time"] = *item.ExpireTime
			}
			if item.Geo != nil {
				entry["geo"] = *item.Geo
			}
			items = append(items, entry)
		}
		payload, err := json.Marshal(map[string]any{"schema_version": "integration.feed.v1", "impression_id": response.ImpressionId, "matching_version": version, "items": items})
		if err != nil {
			return output, err
		}
		return integrationfeed.Generated{Payload: payload, ItemCount: len(items), MatchingVersion: version}, nil
	}
}
