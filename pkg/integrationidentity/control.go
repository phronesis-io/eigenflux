package integrationidentity

import (
	"context"
	"errors"
	"fmt"
	"gorm.io/gorm"
)

// Check always reads current control state. It deliberately has no authorization
// cache: the 90-second SDK permit never authorizes an otherwise disabled server request.
func (s *Store) Check(ctx context.Context, appID, installationID, instanceID string) (Policy, Agent, error) {
	var row struct {
		Agent
		GlobalEnabled       bool
		GlobalRevision      int64
		AppEnabled          bool
		AppRevision         int64
		MinInterval         int
		InstallationEnabled bool
		InstanceID          string
	}
	if s.DB == nil {
		return Policy{}, Agent{}, unavailable()
	}
	result := s.DB.WithContext(ctx).Raw(`SELECT a.*, c.enabled AS global_enabled,
		c.revision AS global_revision, p.enabled AS app_enabled, p.revision AS app_revision,
		p.min_poll_interval_seconds AS min_interval, i.enabled AS installation_enabled,
		i.runtime_instance_id AS instance_id
		FROM integration_agents a JOIN integration_apps p ON p.app_id = a.app_id
		JOIN integration_installations i ON i.agent_id = a.agent_id
		CROSS JOIN integration_control c
		WHERE a.app_id = ? AND i.installation_id = ? AND c.id = 1`, appID, installationID).Scan(&row)
	if result.Error != nil {
		return Policy{}, Agent{}, unavailable()
	}
	if result.RowsAffected == 0 {
		// Distinguish a missing control singleton (storage/config failure) from
		// an installation not owned by this authenticated application.
		var controls int64
		if err := s.DB.WithContext(ctx).Table("integration_control").Where("id = 1").Count(&controls).Error; err != nil || controls != 1 {
			return Policy{}, Agent{}, unavailable()
		}
		return Policy{}, Agent{}, deny("INSTALLATION_NOT_FOUND")
	}
	p := policy(row.GlobalEnabled, row.GlobalRevision, App{Enabled: row.AppEnabled, Revision: row.AppRevision, MinPollIntervalSeconds: row.MinInterval}, row.Agent, Installation{Enabled: row.InstallationEnabled})
	if instanceID == "" || row.InstanceID != instanceID {
		p.BusinessEnabled = false
		p.Reason = "INSTANCE_SUPERSEDED"
	} else {
		now, err := serverNow(s.DB.WithContext(ctx))
		if err != nil {
			return Policy{}, Agent{}, unavailable()
		}
		// Online observation is separate from permission. Disabled instances may
		// still check control; only the currently registered process renews it.
		if err := s.DB.WithContext(ctx).Model(&Installation{}).Where("installation_id = ? AND runtime_instance_id = ? AND last_seen_at < ?", installationID, instanceID, now).Update("last_seen_at", now).Error; err != nil {
			return Policy{}, Agent{}, unavailable()
		}
	}
	return p, row.Agent, nil
}

// RequireBusiness is shared by context, Feed, and future delivery endpoints.
func (s *Store) RequireBusiness(ctx context.Context, appID, installationID, instanceID string) (Agent, error) {
	p, agent, err := s.Check(ctx, appID, installationID, instanceID)
	if err != nil {
		return Agent{}, err
	}
	if !p.BusinessEnabled {
		return Agent{}, deny(p.Reason)
	}
	return agent, nil
}

// SetEnabled is an operator-only operation; it is not registered as a customer
// HTTP route. Every update and its audit record commit in the same transaction.
func (s *Store) SetEnabled(ctx context.Context, appID string, agentID int64, enabled bool, actor, reason string) error {
	if actor == "" || reason == "" || len(actor) > 128 || len(reason) > 512 {
		return &Error{400, "INVALID_OPERATOR_CHANGE"}
	}
	if s.DB == nil {
		return unavailable()
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now, err := serverNow(tx)
		if err != nil {
			return err
		}
		table := "integration_control"
		predicate := "id = ?"
		key := any(1)
		if appID != "" {
			table = "integration_apps"
			predicate = "app_id = ?"
			key = appID
		}
		if agentID != 0 {
			if appID == "" {
				return &Error{400, "APP_ID_REQUIRED"}
			}
			table = "integration_agents"
			predicate = "agent_id = ? AND app_id = ?"
		}
		q := tx.Table(table)
		if agentID != 0 {
			q = q.Where(predicate, agentID, appID)
		} else {
			q = q.Where(predicate, key)
		}
		values := map[string]any{"enabled": enabled, "revision": gorm.Expr("revision + 1"), "updated_at": now}
		if table == "integration_control" {
			values["actor"] = actor
			values["reason"] = reason
		}
		result := q.Updates(values)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("integration control target not found")
		}
		operation := fmt.Sprintf("%s.enabled=%t", table, enabled)
		return tx.Table("integration_audit").Create(map[string]any{"app_id": appID, "agent_id": agentID, "actor": actor, "operation": operation, "reason": reason, "occurred_at": now}).Error
	})
}
