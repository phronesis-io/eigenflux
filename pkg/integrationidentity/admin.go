package integrationidentity

import (
	"context"
	"fmt"
	"gorm.io/gorm"
	"regexp"
	"strings"
)

var appIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

// CreateApp is an operator-side API, not an HTTP customer capability. The
// credential is returned once, after its verification value has committed.
func (s *Store) CreateApp(ctx context.Context, app App, actor string) (string, error) {
	if !appIDPattern.MatchString(app.AppID) || strings.TrimSpace(app.Name) == "" || len(app.Name) > 256 || strings.TrimSpace(actor) == "" || len(actor) > 128 || app.MaxAgents < 1 || app.MaxInstallationsPerAgent < 1 || app.MinPollIntervalSeconds < 10 || app.MinPollIntervalSeconds > 86400 {
		return "", &Error{400, "INVALID_APPLICATION_POLICY"}
	}
	if s.DB == nil {
		return "", unavailable()
	}
	secret, hash, err := NewCredential()
	if err != nil {
		return "", err
	}
	app.CredentialHash = hash
	app.Enabled = false
	app.Revision = 1
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now, err := serverNow(tx)
		if err != nil {
			return err
		}
		app.CreatedAt = now
		app.UpdatedAt = now
		if err := tx.Create(&app).Error; err != nil {
			return err
		}
		return tx.Table("integration_audit").Create(map[string]any{"app_id": app.AppID, "actor": actor, "operation": "app_created_disabled", "occurred_at": now}).Error
	})
	if err != nil {
		return "", err
	}
	return secret, nil
}

// RotateCredential preserves application identity and quotas. It immediately
// invalidates the previous credential for subsequent authenticated requests.
func (s *Store) RotateCredential(ctx context.Context, appID, actor, reason string) (string, error) {
	if strings.TrimSpace(actor) == "" || strings.TrimSpace(reason) == "" || len(actor) > 128 || len(reason) > 512 {
		return "", &Error{400, "INVALID_OPERATOR_CHANGE"}
	}
	if s.DB == nil {
		return "", unavailable()
	}
	secret, hash, err := NewCredential()
	if err != nil {
		return "", err
	}
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now, err := serverNow(tx)
		if err != nil {
			return err
		}
		r := tx.Model(&App{}).Where("app_id = ?", appID).Updates(map[string]any{"credential_hash": hash, "revision": gorm.Expr("revision + 1"), "updated_at": now})
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 {
			return fmt.Errorf("application not found")
		}
		return tx.Table("integration_audit").Create(map[string]any{"app_id": appID, "actor": actor, "operation": "credential_rotated", "reason": reason, "occurred_at": now}).Error
	})
	if err != nil {
		return "", err
	}
	return secret, nil
}

// SetInstallationEnabled revokes one installation without deleting identity or
// freeing a quota slot. It advances the Agent policy fence and records an audit.
func (s *Store) SetInstallationEnabled(ctx context.Context, appID, installationID string, enabled bool, actor, reason string) error {
	if strings.TrimSpace(actor) == "" || strings.TrimSpace(reason) == "" || len(actor) > 128 || len(reason) > 512 {
		return &Error{400, "INVALID_OPERATOR_CHANGE"}
	}
	if s.DB == nil {
		return unavailable()
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row Installation
		if err := tx.Where("installation_id = ? AND agent_id IN (SELECT agent_id FROM integration_agents WHERE app_id = ?)", installationID, appID).First(&row).Error; err != nil {
			return err
		}
		now, err := serverNow(tx)
		if err != nil {
			return err
		}
		if err := tx.Model(&row).Update("enabled", enabled).Error; err != nil {
			return err
		}
		if err := tx.Model(&Agent{}).Where("agent_id = ?", row.AgentID).Updates(map[string]any{"revision": gorm.Expr("revision + 1"), "updated_at": now}).Error; err != nil {
			return err
		}
		return tx.Table("integration_audit").Create(map[string]any{"app_id": appID, "agent_id": row.AgentID, "actor": actor, "operation": fmt.Sprintf("installation.enabled=%t", enabled), "reason": reason, "occurred_at": now}).Error
	})
}
