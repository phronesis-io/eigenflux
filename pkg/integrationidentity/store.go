package integrationidentity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"eigenflux_server/pkg/agentidentity"
	"eigenflux_server/pkg/reqinfo"
	"eigenflux_server/pkg/runtimeidentity"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	gormlogger "gorm.io/gorm/logger"
)

type Error struct {
	Status int
	Code   string
}

func (e *Error) Error() string { return e.Code }
func deny(code string) error   { return &Error{403, code} }
func unavailable() error       { return &Error{503, "INTEGRATION_UNAVAILABLE"} }

type App struct {
	AppID                    string `gorm:"primaryKey" json:"app_id"`
	Name                     string `json:"name"`
	Enabled                  bool   `json:"enabled"`
	CredentialHash           string `json:"-"`
	MaxAgents                int    `json:"max_agents"`
	MaxInstallationsPerAgent int    `json:"max_installations_per_agent"`
	MinPollIntervalSeconds   int    `json:"min_poll_interval_seconds"`
	Revision                 int64  `json:"revision"`
	CreatedAt                int64  `json:"created_at"`
	UpdatedAt                int64  `json:"updated_at"`
}

func (App) TableName() string { return "integration_apps" }

type Agent struct {
	AgentID                      int64  `gorm:"primaryKey" json:"agent_id,string"`
	AppID                        string `json:"app_id"`
	ExternalAgentID              string `json:"external_agent_id"`
	Enabled                      bool   `json:"enabled"`
	RequestedPollIntervalSeconds int    `json:"requested_poll_interval_seconds"`
	NextPollAt                   int64  `json:"next_poll_at"`
	RuntimeName                  string `json:"runtime_name"`
	RuntimeVersion               string `json:"runtime_version"`
	Revision                     int64  `json:"revision"`
	CreatedAt                    int64  `json:"created_at"`
	UpdatedAt                    int64  `json:"updated_at"`
}

func (Agent) TableName() string { return "integration_agents" }

type Installation struct {
	InstallationID    string `gorm:"primaryKey" json:"installation_id"`
	AgentID           int64  `json:"agent_id,string"`
	Enabled           bool   `json:"enabled"`
	SDKVersion        string `json:"sdk_version"`
	RuntimeInstanceID string `json:"runtime_instance_id"`
	LastSeenAt        int64  `json:"last_seen_at"`
	CreatedAt         int64  `json:"created_at"`
}

func (Installation) TableName() string { return "integration_installations" }

type Store struct {
	DB  *gorm.DB
	IDs interface{ NextID() (int64, error) }
}

func (s *Store) Authenticate(ctx context.Context, secret string) (App, error) {
	hash, err := CredentialHash(secret)
	if err != nil {
		return App{}, &Error{401, "INTEGRATION_AUTH_REQUIRED"}
	}
	if s.DB == nil {
		return App{}, unavailable()
	}
	var app App
	// Authentication failures must not log the secret's verification value in SQL.
	err = s.DB.WithContext(ctx).Session(&gorm.Session{Logger: s.DB.Logger.LogMode(gormlogger.Silent)}).Where("credential_hash = ?", hash).First(&app).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return App{}, &Error{401, "INTEGRATION_AUTH_REQUIRED"}
	}
	if err != nil {
		return App{}, unavailable()
	}
	return app, nil
}

type Registration struct {
	ExternalAgentID         string `json:"external_agent_id"`
	RuntimeName             string `json:"runtime_name"`
	RuntimeVersion          string `json:"runtime_version"`
	SDKVersion              string `json:"sdk_version"`
	RuntimeInstanceID       string `json:"runtime_instance_id"`
	InstallationID          string `json:"installation_id,omitempty"`
	FeedPollIntervalSeconds int    `json:"feed_poll_interval_seconds,omitempty"`
}

func (r Registration) validate() error {
	if r.ExternalAgentID == "" || len(r.ExternalAgentID) > 128 || strings.TrimSpace(r.ExternalAgentID) != r.ExternalAgentID ||
		r.RuntimeInstanceID == "" || len(r.RuntimeInstanceID) > 128 || r.SDKVersion == "" || len(r.SDKVersion) > 32 ||
		r.RuntimeName == "" || r.RuntimeVersion == "" {
		return &Error{400, "INVALID_INTEGRATION_REGISTRATION"}
	}
	for _, value := range []string{r.ExternalAgentID, r.RuntimeInstanceID} {
		if !utf8.ValidString(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return &Error{400, "INVALID_INTEGRATION_REGISTRATION"}
		}
	}
	if reqinfo.SafePluginVersion(r.SDKVersion) != r.SDKVersion {
		return &Error{400, "INVALID_SDK_VERSION"}
	}
	identity, ok := runtimeidentity.Parse(r.RuntimeName + "/" + r.RuntimeVersion)
	if !ok || identity.Name != r.RuntimeName || identity.Version != r.RuntimeVersion {
		return &Error{400, "INVALID_RUNTIME_IDENTITY"}
	}
	if r.InstallationID != "" {
		if _, err := uuid.Parse(r.InstallationID); err != nil {
			return &Error{400, "INVALID_INSTALLATION_ID"}
		}
	}
	if r.FeedPollIntervalSeconds != 0 && (r.FeedPollIntervalSeconds < 10 || r.FeedPollIntervalSeconds > 86400) {
		return &Error{400, "INVALID_POLL_INTERVAL"}
	}
	return nil
}

type RegistrationResult struct {
	Agent        Agent        `json:"agent"`
	Installation Installation `json:"installation"`
	Policy       Policy       `json:"policy"`
}
type Policy struct {
	BusinessEnabled                  bool   `json:"business_enabled"`
	Reason                           string `json:"reason"`
	PolicyRevision                   string `json:"policy_revision"`
	EffectiveFeedPollIntervalSeconds int    `json:"effective_feed_poll_interval_seconds"`
	NextPollAt                       int64  `json:"next_poll_at"`
	NextControlCheckSeconds          int    `json:"next_control_check_seconds"`
	ValidForSeconds                  int    `json:"valid_for_seconds"`
}

func serverNow(tx *gorm.DB) (int64, error) {
	query := "SELECT (extract(epoch FROM clock_timestamp()) * 1000)::bigint"
	if tx.Dialector.Name() == "sqlite" {
		query = "SELECT CAST(strftime('%s','now') AS INTEGER) * 1000"
	}
	var now int64
	err := tx.Raw(query).Scan(&now).Error
	return now, err
}

// Register serializes each application's identity and installation admissions.
// Disabled rows retain their slots; changing external IDs never frees capacity.
func (s *Store) Register(ctx context.Context, appID string, req Registration) (RegistrationResult, error) {
	var out RegistrationResult
	if err := req.validate(); err != nil {
		return out, err
	}
	if req.InstallationID != "" {
		parsed, _ := uuid.Parse(req.InstallationID) // validate already checked it
		req.InstallationID = parsed.String()
	}
	if s.DB == nil || s.IDs == nil {
		return out, unavailable()
	}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var app App
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&app, "app_id = ?", appID).Error; err != nil {
			return unavailable()
		}
		if !app.Enabled {
			return deny("APP_DISABLED")
		}
		var control struct {
			Enabled  bool
			Revision int64
		}
		if err := tx.Table("integration_control").First(&control, "id = 1").Error; err != nil {
			return unavailable()
		}
		if !control.Enabled {
			return deny("INTEGRATIONS_DISABLED")
		}
		now, err := serverNow(tx)
		if err != nil {
			return unavailable()
		}
		agent := Agent{}
		err = tx.Where("app_id = ? AND external_agent_id = ?", appID, req.ExternalAgentID).First(&agent).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			var count int64
			if err := tx.Model(&Agent{}).Where("app_id = ?", appID).Count(&count).Error; err != nil {
				return unavailable()
			}
			if count >= int64(app.MaxAgents) {
				return deny("AGENT_LIMIT_REACHED")
			}
			id, err := s.IDs.NextID()
			if err != nil {
				return unavailable()
			}
			if err := agentidentity.InsertAnonymous(tx, id, "integration-"+uuid.NewString()+"@identity.invalid", req.RuntimeName, now); err != nil {
				return err
			}
			if err := tx.Exec("INSERT INTO agent_profiles(agent_id, status, updated_at) VALUES (?, 0, ?)", id, now).Error; err != nil {
				return err
			}
			interval := req.FeedPollIntervalSeconds
			if interval == 0 {
				interval = 3600
			}
			agent = Agent{AgentID: id, AppID: appID, ExternalAgentID: req.ExternalAgentID, Enabled: true, RequestedPollIntervalSeconds: interval, RuntimeName: req.RuntimeName, RuntimeVersion: req.RuntimeVersion, Revision: 1, CreatedAt: now, UpdatedAt: now}
			if err := tx.Create(&agent).Error; err != nil {
				return err
			}
		} else if err != nil {
			return unavailable()
		}
		if !agent.Enabled {
			return deny("AGENT_DISABLED")
		}
		installation := Installation{}
		q := tx.Where("agent_id = ?", agent.AgentID)
		if req.InstallationID != "" {
			q = q.Where("installation_id = ?", req.InstallationID)
		}
		err = q.Order("created_at, installation_id").First(&installation).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			var count int64
			if err := tx.Model(&Installation{}).Where("agent_id = ?", agent.AgentID).Count(&count).Error; err != nil {
				return unavailable()
			}
			if count >= int64(app.MaxInstallationsPerAgent) {
				return deny("INSTALLATION_LIMIT_REACHED")
			}
			id := req.InstallationID
			if id == "" {
				id = uuid.NewString()
			}
			installation = Installation{InstallationID: id, AgentID: agent.AgentID, Enabled: true, SDKVersion: req.SDKVersion, RuntimeInstanceID: req.RuntimeInstanceID, CreatedAt: now, LastSeenAt: now}
			if err := tx.Create(&installation).Error; err != nil {
				return err
			}
		} else if err != nil {
			return unavailable()
		}
		if !installation.Enabled {
			return deny("INSTALLATION_REVOKED")
		}
		interval := req.FeedPollIntervalSeconds
		if interval == 0 {
			interval = 3600
		}
		if err := tx.Model(&agent).Updates(map[string]any{"runtime_name": req.RuntimeName, "runtime_version": req.RuntimeVersion, "requested_poll_interval_seconds": interval, "updated_at": now}).Error; err != nil {
			return err
		}
		if err := tx.Model(&installation).Updates(map[string]any{"sdk_version": req.SDKVersion, "runtime_instance_id": req.RuntimeInstanceID, "last_seen_at": now}).Error; err != nil {
			return err
		}
		out = RegistrationResult{agent, installation, policy(control.Enabled, control.Revision, app, agent, installation)}
		return nil
	})
	if err != nil {
		var typed *Error
		if !errors.As(err, &typed) {
			err = unavailable()
		}
	}
	return out, err
}

func policy(enabled bool, revision int64, app App, agent Agent, installation Installation) Policy {
	p := Policy{BusinessEnabled: true, PolicyRevision: fmt.Sprintf("%d:%d:%d", revision, app.Revision, agent.Revision), EffectiveFeedPollIntervalSeconds: max(app.MinPollIntervalSeconds, agent.RequestedPollIntervalSeconds), NextPollAt: agent.NextPollAt, NextControlCheckSeconds: 30, ValidForSeconds: 90}
	switch {
	case !enabled:
		p.Reason = "INTEGRATIONS_DISABLED"
	case !app.Enabled:
		p.Reason = "APP_DISABLED"
	case !agent.Enabled:
		p.Reason = "AGENT_DISABLED"
	case !installation.Enabled:
		p.Reason = "INSTALLATION_REVOKED"
	}
	p.BusinessEnabled = p.Reason == ""
	return p
}
