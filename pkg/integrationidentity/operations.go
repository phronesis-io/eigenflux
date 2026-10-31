package integrationidentity

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

type AppPolicy struct {
	MaxAgents                int `json:"max_agents"`
	MaxInstallationsPerAgent int `json:"max_installations_per_agent"`
	MinPollIntervalSeconds   int `json:"min_poll_interval_seconds"`
	MaxFeedBatchesPerDay     int `json:"max_feed_batches_per_day"`
}

func (s *Store) AppPolicy(ctx context.Context, appID string) (AppPolicy, error) {
	var policy AppPolicy
	if s.DB == nil {
		return policy, unavailable()
	}
	err := s.DB.WithContext(ctx).Table("integration_apps").Select("max_agents,max_installations_per_agent,min_poll_interval_seconds,max_feed_batches_per_day").Where("app_id = ?", appID).First(&policy).Error
	return policy, err
}

// SetAppPolicy changes future admission without deleting existing identities,
// freeing occupied slots, resetting usage counters or re-enabling the app.
func (s *Store) SetAppPolicy(ctx context.Context, appID string, policy AppPolicy, actor, reason string) error {
	if strings.TrimSpace(actor) == "" || strings.TrimSpace(reason) == "" || len(actor) > 128 || len(reason) > 512 || policy.MaxAgents < 1 || policy.MaxInstallationsPerAgent < 1 || policy.MinPollIntervalSeconds < 10 || policy.MinPollIntervalSeconds > 86400 || policy.MaxFeedBatchesPerDay < 1 {
		return &Error{400, "INVALID_APPLICATION_POLICY"}
	}
	if s.DB == nil {
		return unavailable()
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var app App
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&app, "app_id = ?", appID).Error; err != nil {
			return err
		}
		now, err := serverNow(tx)
		if err != nil {
			return err
		}
		if err := tx.Table("integration_apps").Where("app_id = ?", appID).Updates(map[string]any{"max_agents": policy.MaxAgents, "max_installations_per_agent": policy.MaxInstallationsPerAgent, "min_poll_interval_seconds": policy.MinPollIntervalSeconds, "max_feed_batches_per_day": policy.MaxFeedBatchesPerDay, "revision": gorm.Expr("revision + 1"), "updated_at": now}).Error; err != nil {
			return err
		}
		raw, _ := json.Marshal(policy)
		return tx.Table("integration_audit").Create(map[string]any{"app_id": appID, "actor": actor, "operation": "application.policy=" + string(raw), "reason": reason, "occurred_at": now}).Error
	})
}

// RecordRequest observes mapped enterprise HTTP requests. Admission counters
// remain the exact quota authority; this operational observation is best effort.
func (s *Store) RecordRequest(ctx context.Context, agentID int64, failure string) error {
	if s.DB == nil {
		return unavailable()
	}
	if len(failure) > 100 {
		return errors.New("invalid failure code")
	}
	db := s.DB.WithContext(ctx).Session(&gorm.Session{Logger: s.DB.Logger.LogMode(logger.Silent)})
	now, err := serverNow(db)
	if err != nil {
		return err
	}
	updates := map[string]any{"request_count": gorm.Expr("request_count + 1"), "last_request_at": now}
	if failure != "" {
		updates["last_failure_code"] = failure
		updates["last_failure_at"] = now
	}
	result := db.Table("integration_agents").Where("agent_id = ?", agentID).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return errors.New("managed Agent not found")
	}
	return nil
}

type AppOverview struct {
	AppID    string `json:"app_id"`
	Name     string `json:"name"`
	Enabled  bool   `json:"enabled"`
	Revision int64  `json:"revision"`
	AppPolicy
	AgentCount       int64 `json:"agent_count"`
	RequestCount     int64 `json:"request_count"`
	FeedUsageDay     int64 `json:"feed_usage_day"`
	FeedBatchesToday int64 `json:"feed_batches_today"`
	FeedBatchesTotal int64 `json:"feed_batches_total"`
}
type AgentOverview struct {
	AgentID                      int64  `json:"agent_id,string"`
	AppID                        string `json:"app_id"`
	ExternalAgentID              string `json:"external_agent_id"`
	Enabled                      bool   `json:"enabled"`
	RuntimeName                  string `json:"runtime_name"`
	RuntimeVersion               string `json:"runtime_version"`
	RequestedPollIntervalSeconds int    `json:"requested_poll_interval_seconds"`
	EffectivePollIntervalSeconds int    `json:"effective_poll_interval_seconds"`
	NextPollAt                   int64  `json:"next_poll_at"`
	RequestCount                 int64  `json:"request_count"`
	LastRequestAt                int64  `json:"last_request_at"`
	LastFailureCode              string `json:"last_failure_code"`
	LastFailureAt                int64  `json:"last_failure_at"`
	ContextStatus                string `json:"context_status"`
	ContextRevision              int64  `json:"context_revision"`
	AppliedMatchingVersion       int64  `json:"applied_matching_version"`
	ContextFailureCode           string `json:"context_failure_code"`
	FeedStatus                   string `json:"feed_status"`
	FeedBatchesTotal             int64  `json:"feed_batches_total"`
	FeedItemCount                int    `json:"feed_item_count"`
	LastFeedAt                   int64  `json:"last_feed_at"`
	FeedFailureCode              string `json:"feed_failure_code"`
}
type Overview struct {
	Global struct {
		Enabled   bool   `json:"enabled"`
		Revision  int64  `json:"revision"`
		Actor     string `json:"actor"`
		Reason    string `json:"reason"`
		UpdatedAt int64  `json:"updated_at"`
	} `json:"global"`
	Applications           []AppOverview   `json:"applications"`
	Agents                 []AgentOverview `json:"agents"`
	Installations          []Installation  `json:"installations"`
	ApplicationsTruncated  bool            `json:"applications_truncated"`
	AgentsTruncated        bool            `json:"agents_truncated"`
	InstallationsTruncated bool            `json:"installations_truncated"`
}

func (s *Store) Inspect(ctx context.Context, appID string, agentID, afterAgentID int64) (Overview, error) {
	result := Overview{Applications: []AppOverview{}, Agents: []AgentOverview{}, Installations: []Installation{}}
	if s.DB == nil {
		return result, unavailable()
	}
	db := s.DB.WithContext(ctx)
	if err := db.Table("integration_control").Where("id = 1").First(&result.Global).Error; err != nil {
		return result, err
	}
	apps := db.Table("integration_apps p").Select(`p.app_id,p.name,p.enabled,p.revision,p.max_agents,p.max_installations_per_agent,p.min_poll_interval_seconds,p.max_feed_batches_per_day,p.feed_usage_day,p.feed_batches_today,p.feed_batches_total,
 (SELECT count(*) FROM integration_agents a WHERE a.app_id=p.app_id) AS agent_count,
 COALESCE((SELECT sum(a.request_count) FROM integration_agents a WHERE a.app_id=p.app_id),0) AS request_count`)
	if appID != "" {
		apps = apps.Where("p.app_id = ?", appID)
	}
	if err := apps.Order("p.app_id").Limit(101).Find(&result.Applications).Error; err != nil {
		return result, err
	}
	if len(result.Applications) > 100 {
		result.ApplicationsTruncated = true
		result.Applications = result.Applications[:100]
	}
	agents := db.Table("integration_agents a").Select(`a.agent_id,a.app_id,a.external_agent_id,a.enabled,a.runtime_name,a.runtime_version,a.requested_poll_interval_seconds,
 CASE WHEN a.requested_poll_interval_seconds>p.min_poll_interval_seconds THEN a.requested_poll_interval_seconds ELSE p.min_poll_interval_seconds END AS effective_poll_interval_seconds,
 a.next_poll_at,a.request_count,a.last_request_at,a.last_failure_code,a.last_failure_at,
 COALESCE(c.status,'none') AS context_status,COALESCE(c.source_revision,0) AS context_revision,COALESCE(c.applied_matching_version,0) AS applied_matching_version,COALESCE(c.failure_code,'') AS context_failure_code,
 COALESCE(f.status,'none') AS feed_status,COALESCE(f.sequence,0) AS feed_batches_total,COALESCE(f.item_count,0) AS feed_item_count,COALESCE(f.created_at,0) AS last_feed_at,COALESCE(f.failure_code,'') AS feed_failure_code`).
		Joins("JOIN integration_apps p ON p.app_id=a.app_id LEFT JOIN integration_contexts c ON c.agent_id=a.agent_id LEFT JOIN integration_feed_batches f ON f.agent_id=a.agent_id")
	if appID != "" {
		agents = agents.Where("a.app_id = ?", appID)
	}
	if agentID != 0 {
		agents = agents.Where("a.agent_id = ?", agentID)
	}
	if err := agents.Where("a.agent_id > ?", afterAgentID).Order("a.agent_id").Limit(101).Find(&result.Agents).Error; err != nil {
		return result, err
	}
	if len(result.Agents) > 100 {
		result.AgentsTruncated = true
		result.Agents = result.Agents[:100]
	}
	ids := make([]int64, 0, len(result.Agents))
	for _, a := range result.Agents {
		ids = append(ids, a.AgentID)
	}
	if len(ids) > 0 {
		if err := db.Where("agent_id IN ?", ids).Order("agent_id,installation_id").Limit(1001).Find(&result.Installations).Error; err != nil {
			return result, err
		}
	}
	if len(result.Installations) > 1000 {
		result.InstallationsTruncated = true
		result.Installations = result.Installations[:1000]
	}
	return result, nil
}
