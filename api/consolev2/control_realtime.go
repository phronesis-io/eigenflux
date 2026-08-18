package consolev2

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/lib/pq"
	"gorm.io/gorm"

	"eigenflux_server/pkg/logger"
)

const (
	controlOutboxBatch   = 50
	controlPublishWorker = 8
)

type controlOutboxRow struct {
	OutboxID     int64 `gorm:"column:outbox_id"`
	AgentID      int64 `gorm:"column:agent_id"`
	EntityID     int64 `gorm:"column:entity_id"`
	AttemptCount int   `gorm:"column:attempt_count"`
}

func (s *Service) claimControlOutbox(now int64) ([]controlOutboxRow, error) {
	var rows []controlOutboxRow
	err := s.db.Transaction(func(tx *gorm.DB) error {
		return tx.Raw(`WITH picked AS (
			SELECT outbox_id FROM control_wakeup_outbox
			WHERE status = 'pending' AND next_attempt_at <= ?
			ORDER BY next_attempt_at, outbox_id
			FOR UPDATE SKIP LOCKED LIMIT ?
		)
		UPDATE control_wakeup_outbox outbox
		SET attempt_count = outbox.attempt_count + 1, next_attempt_at = ?
		FROM picked WHERE outbox.outbox_id = picked.outbox_id
		RETURNING outbox.outbox_id, outbox.agent_id, outbox.entity_id, outbox.attempt_count`,
			now, controlOutboxBatch, now+30_000).Scan(&rows).Error
	})
	return rows, err
}

func (s *Service) dispatchControlOutbox(rows []controlOutboxRow) {
	if len(rows) == 0 {
		return
	}
	jobs := make(chan controlOutboxRow)
	var workers sync.WaitGroup
	var resultMu sync.Mutex
	succeeded := make([]int64, 0, len(rows))
	failed := make([]int64, 0)
	for worker := 0; worker < controlPublishWorker && worker < len(rows); worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for row := range jobs {
				publishContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				err := s.redisClient.Publish(publishContext,
					fmt.Sprintf("console:v2:control:wakeup:%d", row.AgentID), strconv.FormatInt(row.EntityID, 10)).Err()
				cancel()
				resultMu.Lock()
				if err == nil {
					succeeded = append(succeeded, row.OutboxID)
				} else {
					failed = append(failed, row.OutboxID)
				}
				resultMu.Unlock()
			}
		}()
	}
	for _, row := range rows {
		jobs <- row
	}
	close(jobs)
	workers.Wait()
	now := time.Now().UnixMilli()
	if len(succeeded) > 0 {
		if err := s.db.Exec(`UPDATE control_wakeup_outbox
			SET status = 'delivered', delivered_at = ?, last_error = NULL
			WHERE outbox_id = ANY(?) AND status = 'pending'`, now, pq.Array(succeeded)).Error; err != nil {
			logger.Default().Error("Console V2 control outbox success update failed", "err", err)
		}
	}
	if len(failed) > 0 {
		if err := s.db.Exec(`UPDATE control_wakeup_outbox SET
			status = CASE WHEN attempt_count >= 10 THEN 'dead' ELSE 'pending' END,
			next_attempt_at = ? + (power(2, LEAST(attempt_count, 6))::bigint * 1000),
			last_error = 'redis publish failed'
			WHERE outbox_id = ANY(?) AND status = 'pending'`, now, pq.Array(failed)).Error; err != nil {
			logger.Default().Error("Console V2 control outbox retry update failed", "err", err)
		}
	}
}

func (s *Service) runControlOutboxDispatcher() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for range ticker.C {
		rows, err := s.claimControlOutbox(time.Now().UnixMilli())
		if err != nil {
			logger.Default().Error("Console V2 control outbox claim failed", "err", err)
			continue
		}
		s.dispatchControlOutbox(rows)
	}
}

func (s *Service) subscribeControlWake(agentID int64) (<-chan int64, func()) {
	wake := make(chan int64, 32)
	s.controlWakeMu.Lock()
	if s.controlWakeSubs[agentID] == nil {
		s.controlWakeSubs[agentID] = make(map[chan int64]struct{})
	}
	s.controlWakeSubs[agentID][wake] = struct{}{}
	s.controlWakeMu.Unlock()
	return wake, func() {
		s.controlWakeMu.Lock()
		delete(s.controlWakeSubs[agentID], wake)
		if len(s.controlWakeSubs[agentID]) == 0 {
			delete(s.controlWakeSubs, agentID)
		}
		close(wake)
		s.controlWakeMu.Unlock()
	}
}

func (s *Service) notifyControlWake(agentID, commandID int64) {
	s.controlWakeMu.RLock()
	defer s.controlWakeMu.RUnlock()
	for wake := range s.controlWakeSubs[agentID] {
		select {
		case wake <- commandID:
		default:
			// A missed hint is harmless: the stream also reconciles from DB every
			// 15 seconds and Runtime heartbeat returns the same pending IDs.
		}
	}
}

func (s *Service) runControlWakeSubscriber() {
	for {
		pubsub := s.redisClient.PSubscribe(context.Background(), "console:v2:control:wakeup:*")
		for message := range pubsub.Channel() {
			agentIDValue, agentErr := strconv.ParseInt(strings.TrimPrefix(message.Channel, "console:v2:control:wakeup:"), 10, 64)
			commandID, commandErr := strconv.ParseInt(message.Payload, 10, 64)
			if agentErr == nil && commandErr == nil && agentIDValue > 0 && commandID > 0 {
				s.notifyControlWake(agentIDValue, commandID)
			}
		}
		_ = pubsub.Close()
		logger.Default().Warn("Console V2 control subscriber reconnecting")
		time.Sleep(time.Second)
	}
}

func (s *Service) pendingCommandIDs(agentID, now int64, limit int) ([]int64, error) {
	var ids []int64
	err := s.db.Raw(`SELECT command_id FROM agent_commands
		WHERE agent_id = ? AND (status IN ('pending','notified') OR (status = 'claimed' AND claim_until <= ?))
		ORDER BY created_at, command_id LIMIT ?`, agentID, now, limit).Scan(&ids).Error
	return ids, err
}

func (s *Service) agentCredentialSessionStillActive(sessionID, agentID int64) bool {
	var active bool
	now := time.Now().UnixMilli()
	err := s.db.Raw(`SELECT EXISTS(SELECT 1 FROM agent_credential_sessions session
		JOIN agent_principals principal ON principal.principal_id = session.principal_id
		WHERE session.session_id = ? AND principal.agent_id = ? AND session.audience = 'agent_v2'
		 AND session.revoked_at IS NULL AND session.expires_at > ?
		 AND principal.revoked_at IS NULL AND principal.status IN ('limited','active'))`,
		sessionID, agentID, now).Scan(&active).Error
	return err == nil && active
}

func (s *Service) streamRuntimeControl(_ context.Context, c *app.RequestContext) {
	agentIDValue, _ := agentID(c)
	sessionValue, ok := c.Get("agent_credential_session_id")
	credentialSessionID, _ := sessionValue.(int64)
	if !ok || credentialSessionID <= 0 {
		fail(c, http.StatusUnauthorized, "AGENT_AUTH_INVALID", "Agent V2 session is unavailable", nil)
		return
	}
	s.controlWakeMu.Lock()
	if s.controlConnections[agentIDValue] >= maxAgentStreams || !s.tryAcquireProcessStream() {
		s.controlWakeMu.Unlock()
		fail(c, http.StatusTooManyRequests, "CONTROL_CONNECTION_LIMIT", "too many control streams for this Agent", nil)
		return
	}
	s.controlConnections[agentIDValue]++
	s.controlTotal++
	s.controlWakeMu.Unlock()
	wake, unsubscribe := s.subscribeControlWake(agentIDValue)
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
			s.controlWakeMu.Lock()
			s.controlConnections[agentIDValue]--
			s.controlTotal--
			if s.controlConnections[agentIDValue] <= 0 {
				delete(s.controlConnections, agentIDValue)
			}
			s.controlWakeMu.Unlock()
			s.releaseProcessStream()
		}()
		sent := make(map[int64]struct{}, 64)
		writePending := func() error {
			ids, err := s.pendingCommandIDs(agentIDValue, time.Now().UnixMilli(), 50)
			if err != nil {
				return err
			}
			for _, commandID := range ids {
				if _, exists := sent[commandID]; exists {
					continue
				}
				payload, _ := json.Marshal(map[string]string{"command_id": strconv.FormatInt(commandID, 10)})
				if err := writeSSE(writer, fmt.Sprintf("id: %d\nevent: command_available\ndata: %s\n\n", commandID, payload)); err != nil {
					return err
				}
				sent[commandID] = struct{}{}
			}
			return nil
		}
		if writePending() != nil {
			return
		}
		reconcile := time.NewTicker(15 * time.Second)
		heartbeat := time.NewTicker(30 * time.Second)
		maxLifetime := time.NewTimer(10 * time.Minute)
		defer reconcile.Stop()
		defer heartbeat.Stop()
		defer maxLifetime.Stop()
		for {
			select {
			case commandID, open := <-wake:
				if !open {
					return
				}
				if _, exists := sent[commandID]; !exists {
					payload, _ := json.Marshal(map[string]string{"command_id": strconv.FormatInt(commandID, 10)})
					if err := writeSSE(writer, fmt.Sprintf("id: %d\nevent: command_available\ndata: %s\n\n", commandID, payload)); err != nil {
						return
					}
					sent[commandID] = struct{}{}
				}
			case <-reconcile.C:
				if writePending() != nil {
					return
				}
			case <-heartbeat.C:
				if !s.agentCredentialSessionStillActive(credentialSessionID, agentIDValue) {
					return
				}
				if err := writeSSE(writer, fmt.Sprintf(": heartbeat %d\n\n", time.Now().UnixMilli())); err != nil {
					return
				}
			case <-maxLifetime.C:
				return
			}
		}
	}()
}
