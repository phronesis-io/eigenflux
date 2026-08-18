package consolev2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/cloudwego/hertz/pkg/app"

	"eigenflux_server/pkg/logger"
)

const (
	activityInitialReplay = 100
	activityReplayPage    = 100
	activityReplayMax     = 500
	sseWriteTimeout       = 5 * time.Second
)

var errActivityReplayTruncated = errors.New("activity replay page truncated")

func writeSSE(writer *io.PipeWriter, payload string) error {
	timer := time.AfterFunc(sseWriteTimeout, func() {
		_ = writer.CloseWithError(errors.New("SSE write timeout"))
	})
	_, err := io.WriteString(writer, payload)
	timer.Stop()
	return err
}

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

func (s *Service) oldestActivitySeq(agentID int64) (int64, error) {
	var minSeq int64
	err := s.db.Raw(`SELECT COALESCE((SELECT agent_seq FROM agent_activity_log
		WHERE agent_id = ? AND agent_seq IS NOT NULL ORDER BY agent_seq LIMIT 1), 0)`, agentID).Scan(&minSeq).Error
	return minSeq, err
}

func (s *Service) latestActivitySeq(agentID int64) (int64, error) {
	var current int64
	err := s.db.Raw(`SELECT COALESCE(current_seq, 0) FROM agent_activity_heads WHERE agent_id = ?`, agentID).Scan(&current).Error
	return current, err
}

func (s *Service) subscribeActivityWake(agentID int64) (<-chan struct{}, func()) {
	wake := make(chan struct{}, 1)
	s.activityWakeMu.Lock()
	if s.activityWakeSubs[agentID] == nil {
		s.activityWakeSubs[agentID] = make(map[chan struct{}]struct{})
	}
	s.activityWakeSubs[agentID][wake] = struct{}{}
	s.activityWakeMu.Unlock()
	return wake, func() {
		s.activityWakeMu.Lock()
		delete(s.activityWakeSubs[agentID], wake)
		if len(s.activityWakeSubs[agentID]) == 0 {
			delete(s.activityWakeSubs, agentID)
		}
		close(wake)
		s.activityWakeMu.Unlock()
	}
}

func (s *Service) notifyActivityWake(agentID int64) {
	s.activityWakeMu.RLock()
	defer s.activityWakeMu.RUnlock()
	for wake := range s.activityWakeSubs[agentID] {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
}

func (s *Service) runActivityWakeSubscriber() {
	for {
		pubsub := s.redisClient.Subscribe(context.Background(), "console:v2:activity:wakeup")
		channel := pubsub.Channel()
		for message := range channel {
			agentIDValue, err := strconv.ParseInt(message.Payload, 10, 64)
			if err == nil && agentIDValue > 0 {
				s.notifyActivityWake(agentIDValue)
			}
		}
		_ = pubsub.Close()
		logger.Default().Warn("Console V2 activity wakeup subscriber reconnecting")
		time.Sleep(time.Second)
	}
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
	if after == 0 {
		latest, latestErr := s.latestActivitySeq(agentIDValue)
		if latestErr != nil {
			fail(c, http.StatusInternalServerError, "ACTIVITY_READ_FAILED", "could not read activity cursor", nil)
			return
		}
		after = maxInt64(0, latest-activityInitialReplay)
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
	minSeq, boundsErr := s.oldestActivitySeq(agentIDValue)
	if boundsErr != nil {
		fail(c, http.StatusInternalServerError, "ACTIVITY_READ_FAILED", "could not read activity cursor", nil)
		return
	}
	cursorReset := after > 0 && minSeq > 0 && after < minSeq-1
	if cursorReset {
		after = minSeq - 1
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
	reply(c, http.StatusOK, map[string]interface{}{
		"events": events, "next_cursor": next, "has_more": len(events) == limit,
		"cursor_reset": cursorReset, "oldest_available_cursor": maxInt64(0, minSeq-1),
	})
}

func (s *Service) streamActivity(_ context.Context, c *app.RequestContext) {
	agentIDValue, _ := agentID(c)
	sessionValue, sessionOK := c.Get("console_session_id")
	sessionID, _ := sessionValue.(string)
	if !sessionOK || sessionID == "" {
		fail(c, http.StatusUnauthorized, "CONSOLE_SESSION_REQUIRED", "Console V2 session is required", nil)
		return
	}
	after, err := parseActivityCursor(c)
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_CURSOR", err.Error(), nil)
		return
	}
	if after == 0 {
		latest, latestErr := s.latestActivitySeq(agentIDValue)
		if latestErr != nil {
			fail(c, http.StatusInternalServerError, "ACTIVITY_READ_FAILED", "could not read activity cursor", nil)
			return
		}
		after = maxInt64(0, latest-activityInitialReplay)
	}
	s.activityMu.Lock()
	if s.activityConnections[agentIDValue] >= maxAgentStreams || !s.tryAcquireProcessStream() {
		s.activityMu.Unlock()
		fail(c, http.StatusTooManyRequests, "ACTIVITY_CONNECTION_LIMIT", "too many activity streams for this Agent", nil)
		return
	}
	s.activityConnections[agentIDValue]++
	s.activityTotal++
	s.activityMu.Unlock()
	wake, unsubscribe := s.subscribeActivityWake(agentIDValue)
	minSeq, boundsErr := s.oldestActivitySeq(agentIDValue)
	if boundsErr != nil {
		unsubscribe()
		s.activityMu.Lock()
		s.activityConnections[agentIDValue]--
		s.activityTotal--
		s.activityMu.Unlock()
		s.releaseProcessStream()
		fail(c, http.StatusInternalServerError, "ACTIVITY_READ_FAILED", "could not read activity cursor", nil)
		return
	}
	cursorReset := after > 0 && minSeq > 0 && after < minSeq-1
	if cursorReset {
		after = minSeq - 1
	}

	reader, writer := io.Pipe()
	c.Header("Content-Type", "text/event-stream; charset=utf-8")
	c.Header("Cache-Control", "private, no-store")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.SetBodyStream(reader, -1)

	go func() {
		defer func() {
			unsubscribe()
			_ = writer.Close()
			s.activityMu.Lock()
			s.activityConnections[agentIDValue]--
			s.activityTotal--
			if s.activityConnections[agentIDValue] <= 0 {
				delete(s.activityConnections, agentIDValue)
			}
			s.activityMu.Unlock()
			s.releaseProcessStream()
		}()
		cursor := after
		poll := time.NewTicker(10 * time.Second)
		heartbeat := time.NewTicker(20 * time.Second)
		sessionCheck := time.NewTicker(30 * time.Second)
		maxLifetime := time.NewTimer(30 * time.Minute)
		defer poll.Stop()
		defer heartbeat.Stop()
		defer sessionCheck.Stop()
		defer maxLifetime.Stop()
		writePending := func() error {
			for delivered := 0; delivered < activityReplayMax; delivered += activityReplayPage {
				events, loadErr := s.loadActivity(agentIDValue, cursor, activityReplayPage)
				if loadErr != nil {
					return loadErr
				}
				for _, event := range events {
					encoded, _ := json.Marshal(event)
					if writeErr := writeSSE(writer, fmt.Sprintf("id: %d\nevent: activity\ndata: %s\n\n", event.AgentSeq, encoded)); writeErr != nil {
						return writeErr
					}
					cursor = event.AgentSeq
				}
				if len(events) < activityReplayPage {
					return nil
				}
			}
			latest, loadErr := s.latestActivitySeq(agentIDValue)
			if loadErr != nil {
				return loadErr
			}
			if latest > cursor {
				encoded, _ := json.Marshal(map[string]interface{}{"resume_after": cursor, "latest_cursor": latest})
				if err := writeSSE(writer, fmt.Sprintf("id: %d\nevent: replay_truncated\ndata: %s\n\n", cursor, encoded)); err != nil {
					return err
				}
				// Close this stream after the bounded page. EventSource reconnects
				// with the last delivered cursor, so the next page continues without
				// skipping the retained events between cursor and latest.
				return errActivityReplayTruncated
			}
			return nil
		}
		if cursorReset {
			encoded, _ := json.Marshal(map[string]interface{}{"oldest_available_cursor": after})
			if err := writeSSE(writer, fmt.Sprintf("id: %d\nevent: cursor_reset\ndata: %s\n\n", after, encoded)); err != nil {
				return
			}
		}
		if err := writePending(); err != nil {
			return
		}
		for {
			select {
			case _, ok := <-wake:
				if !ok || writePending() != nil {
					return
				}
			case <-poll.C:
				if err := writePending(); err != nil {
					return
				}
			case <-heartbeat.C:
				if err := writeSSE(writer, fmt.Sprintf(": heartbeat %d\n\n", time.Now().UnixMilli())); err != nil {
					return
				}
			case <-sessionCheck.C:
				if !s.consoleSessionStillActive(sessionID, agentIDValue) {
					return
				}
			case <-maxLifetime.C:
				return
			}
		}
	}()
}

func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}
