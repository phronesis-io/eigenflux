package consolev2

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
)

type activityEvent struct {
	AgentSeq   int64                  `gorm:"column:agent_seq" json:"agent_seq"`
	LogID      int64                  `gorm:"column:log_id" json:"log_id,string"`
	EventType  string                 `gorm:"column:event_type" json:"event_type"`
	Summary    string                 `gorm:"column:summary" json:"summary"`
	Detail     map[string]interface{} `gorm:"-" json:"detail"`
	DetailJSON string                 `gorm:"column:detail" json:"-"`
	CreatedAt  int64                  `gorm:"column:created_at" json:"created_at"`
}

func (s *Service) loadActivity(agentID, after int64, limit int) ([]activityEvent, error) {
	var events []activityEvent
	if err := s.db.Raw(`SELECT agent_seq, log_id, event_type, COALESCE(summary, '') AS summary,
		COALESCE(detail, '{}'::jsonb)::text AS detail, created_at
		FROM agent_activity_log
		WHERE agent_id = ? AND agent_seq > ?
		ORDER BY agent_seq ASC LIMIT ?`, agentID, after, limit).Scan(&events).Error; err != nil {
		return nil, err
	}
	for index := range events {
		events[index].Detail = map[string]interface{}{}
		_ = json.Unmarshal([]byte(events[index].DetailJSON), &events[index].Detail)
	}
	return events, nil
}

func parseActivityCursor(c *app.RequestContext) (int64, error) {
	raw := string(c.GetHeader("Last-Event-ID"))
	if raw == "" {
		raw = c.Query("after")
	}
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("invalid activity cursor")
	}
	return value, nil
}

func (s *Service) listActivity(_ context.Context, c *app.RequestContext) {
	agentIDValue, _ := agentID(c)
	after, err := parseActivityCursor(c)
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_CURSOR", err.Error(), nil)
		return
	}
	limit := 100
	if raw := c.Query("limit"); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed < 1 || parsed > 200 {
			fail(c, http.StatusBadRequest, "INVALID_LIMIT", "limit must be between 1 and 200", nil)
			return
		}
		limit = parsed
	}
	events, err := s.loadActivity(agentIDValue, after, limit)
	if err != nil {
		fail(c, http.StatusInternalServerError, "ACTIVITY_READ_FAILED", "could not read activity", nil)
		return
	}
	next := after
	if len(events) > 0 {
		next = events[len(events)-1].AgentSeq
	}
	reply(c, http.StatusOK, map[string]interface{}{"events": events, "next_cursor": next, "has_more": len(events) == limit})
}

func (s *Service) streamActivity(_ context.Context, c *app.RequestContext) {
	agentIDValue, _ := agentID(c)
	after, err := parseActivityCursor(c)
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_CURSOR", err.Error(), nil)
		return
	}
	s.activityMu.Lock()
	if s.activityConnections[agentIDValue] >= 3 {
		s.activityMu.Unlock()
		fail(c, http.StatusTooManyRequests, "ACTIVITY_CONNECTION_LIMIT", "too many activity streams for this Agent", nil)
		return
	}
	s.activityConnections[agentIDValue]++
	s.activityMu.Unlock()

	reader, writer := io.Pipe()
	c.Header("Content-Type", "text/event-stream; charset=utf-8")
	c.Header("Cache-Control", "private, no-store")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.SetBodyStream(reader, -1)

	go func() {
		defer func() {
			_ = writer.Close()
			s.activityMu.Lock()
			s.activityConnections[agentIDValue]--
			if s.activityConnections[agentIDValue] <= 0 {
				delete(s.activityConnections, agentIDValue)
			}
			s.activityMu.Unlock()
		}()
		cursor := after
		poll := time.NewTicker(10 * time.Second)
		heartbeat := time.NewTicker(20 * time.Second)
		maxLifetime := time.NewTimer(30 * time.Minute)
		defer poll.Stop()
		defer heartbeat.Stop()
		defer maxLifetime.Stop()
		writePending := func() error {
			for {
				events, loadErr := s.loadActivity(agentIDValue, cursor, 100)
				if loadErr != nil {
					return loadErr
				}
				for _, event := range events {
					encoded, _ := json.Marshal(event)
					if _, writeErr := fmt.Fprintf(writer, "id: %d\nevent: activity\ndata: %s\n\n", event.AgentSeq, encoded); writeErr != nil {
						return writeErr
					}
					cursor = event.AgentSeq
				}
				if len(events) < 100 {
					return nil
				}
			}
		}
		if err := writePending(); err != nil {
			return
		}
		for {
			select {
			case <-poll.C:
				if err := writePending(); err != nil {
					return
				}
			case <-heartbeat.C:
				if _, err := fmt.Fprintf(writer, ": heartbeat %d\n\n", time.Now().UnixMilli()); err != nil {
					return
				}
			case <-maxLifetime.C:
				return
			}
		}
	}()
}
