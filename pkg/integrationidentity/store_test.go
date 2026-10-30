package integrationidentity

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type testIDs struct{ value atomic.Int64 }

func (ids *testIDs) NextID() (int64, error) { return ids.value.Add(1), nil }

func testStore(t *testing.T) (*Store, string) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", uuid.NewString())), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = conn.Close() })
	for _, query := range []string{
		`CREATE TABLE agents(agent_id INTEGER PRIMARY KEY,short_id TEXT,email TEXT UNIQUE,email_kind TEXT,agent_name TEXT,bio TEXT,created_at BIGINT,updated_at BIGINT)`,
		`CREATE UNIQUE INDEX agent_short ON agents(short_id) WHERE short_id IS NOT NULL`,
		`CREATE TABLE agent_profiles(agent_id INTEGER PRIMARY KEY,status INTEGER,updated_at BIGINT)`,
		`CREATE TABLE integration_control(id INTEGER PRIMARY KEY,enabled BOOLEAN,revision BIGINT,actor TEXT,reason TEXT,updated_at BIGINT)`,
		`INSERT INTO integration_control VALUES(1,true,1,'test','',0)`,
		`CREATE TABLE integration_audit(id INTEGER PRIMARY KEY,app_id TEXT,agent_id BIGINT,actor TEXT,operation TEXT,reason TEXT,occurred_at BIGINT)`,
	} {
		if err := db.Exec(query).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.AutoMigrate(&App{}, &Agent{}, &Installation{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX stable_external ON integration_agents(app_id,external_agent_id)`).Error; err != nil {
		t.Fatal(err)
	}
	secret, hash, err := NewCredential()
	if err != nil {
		t.Fatal(err)
	}
	app := App{AppID: "partner", Name: "Partner", Enabled: true, CredentialHash: hash, MaxAgents: 1, MaxInstallationsPerAgent: 1, MinPollIntervalSeconds: 3600, Revision: 1}
	if err := db.Create(&app).Error; err != nil {
		t.Fatal(err)
	}
	return &Store{DB: db, IDs: &testIDs{}}, secret
}

func registration() Registration {
	return Registration{ExternalAgentID: "customer-agent", RuntimeName: "jarvis", RuntimeVersion: "2.0", SDKVersion: "1.0.0", RuntimeInstanceID: "run-1"}
}
func requireCode(t *testing.T, err error, status int, code string) {
	t.Helper()
	var typed *Error
	if !errors.As(err, &typed) || typed.Status != status || typed.Code != code {
		t.Fatalf("error=%v, expected %d/%s", err, status, code)
	}
}

func TestStableAnonymousRegistrationAndQuotaRollback(t *testing.T) {
	s, secret := testStore(t)
	ctx := context.Background()
	app, err := s.Authenticate(ctx, secret)
	if err != nil || app.AppID != "partner" {
		t.Fatal(app, err)
	}
	first, err := s.Register(ctx, app.AppID, registration())
	if err != nil {
		t.Fatal(err)
	}
	if first.Agent.RequestedPollIntervalSeconds != 3600 || first.Policy.EffectiveFeedPollIntervalSeconds != 3600 || !first.Policy.BusinessEnabled {
		t.Fatalf("unexpected registration %+v", first)
	}
	req := registration()
	req.RuntimeInstanceID = "run-2"
	req.RuntimeVersion = "2.1"
	second, err := s.Register(ctx, app.AppID, req)
	if err != nil {
		t.Fatal(err)
	}
	if first.Agent.AgentID != second.Agent.AgentID || first.Installation.InstallationID != second.Installation.InstallationID {
		t.Fatal("restart changed identity or installation")
	}
	req.ExternalAgentID = "new-id"
	_, err = s.Register(ctx, app.AppID, req)
	requireCode(t, err, 403, "AGENT_LIMIT_REACHED")
	var identities int64
	if err := s.DB.Table("agents").Count(&identities).Error; err != nil || identities != 1 {
		t.Fatal("over-quota registration left identity", identities, err)
	}
	req = registration()
	req.InstallationID = uuid.NewString()
	_, err = s.Register(ctx, app.AppID, req)
	requireCode(t, err, 403, "INSTALLATION_LIMIT_REACHED")
}

func TestCurrentControlsAndOwnerIsolation(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	r, err := s.Register(ctx, "partner", registration())
	if err != nil {
		t.Fatal(err)
	}
	check := func(want bool, reason string) {
		t.Helper()
		p, _, err := s.Check(ctx, "partner", r.Installation.InstallationID, "run-1")
		if err != nil || p.BusinessEnabled != want || p.Reason != reason || p.ValidForSeconds != 90 || p.NextControlCheckSeconds != 30 {
			t.Fatalf("policy=%+v error=%v", p, err)
		}
	}
	check(true, "")
	if err := s.SetInstallationEnabled(ctx, "partner", r.Installation.InstallationID, false, "operator", "revoke installation"); err != nil {
		t.Fatal(err)
	}
	check(false, "INSTALLATION_REVOKED")
	_, err = s.Register(ctx, "partner", registration())
	requireCode(t, err, 403, "INSTALLATION_REVOKED")
	if err := s.SetInstallationEnabled(ctx, "partner", r.Installation.InstallationID, true, "operator", "restore installation"); err != nil {
		t.Fatal(err)
	}
	check(true, "")
	if err := s.SetEnabled(ctx, "partner", r.Agent.AgentID, false, "operator", "test disable"); err != nil {
		t.Fatal(err)
	}
	check(false, "AGENT_DISABLED")
	_, err = s.RequireBusiness(ctx, "partner", r.Installation.InstallationID, "run-1")
	requireCode(t, err, 403, "AGENT_DISABLED")
	if err := s.SetEnabled(ctx, "partner", r.Agent.AgentID, true, "operator", "test restore"); err != nil {
		t.Fatal(err)
	}
	check(true, "")
	if err := s.SetEnabled(ctx, "partner", 0, false, "operator", "app stop"); err != nil {
		t.Fatal(err)
	}
	check(false, "APP_DISABLED")
	if err := s.SetEnabled(ctx, "", 0, false, "operator", "global stop"); err != nil {
		t.Fatal(err)
	}
	check(false, "INTEGRATIONS_DISABLED")
	_, _, err = s.Check(ctx, "other", r.Installation.InstallationID, "run-1")
	requireCode(t, err, 403, "INSTALLATION_NOT_FOUND")
	if err := s.DB.Exec("DROP TABLE integration_control").Error; err != nil {
		t.Fatal(err)
	}
	_, _, err = s.Check(ctx, "partner", r.Installation.InstallationID, "run-1")
	requireCode(t, err, 503, "INTEGRATION_UNAVAILABLE")
}

func TestRegistrationDoesNotGrantPersonalScopes(t *testing.T) {
	s, _ := testStore(t)
	if _, err := s.Register(context.Background(), "partner", registration()); err != nil {
		t.Fatal(err)
	}
	// The minimal fixture intentionally has no principal/onboarding tables.
	// Managed identity creation must not require or grant either lifecycle.
	if s.DB.Migrator().HasTable("agent_principals") || s.DB.Migrator().HasTable("agent_onboarding_v2") {
		t.Fatal("personal lifecycle created")
	}
}

func TestOperatorApplicationCreationAndCredentialRotation(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	secret, err := s.CreateApp(ctx, App{AppID: "second", Name: "Second application", Enabled: true, MaxAgents: 1, MaxInstallationsPerAgent: 1, MinPollIntervalSeconds: 3600}, "operator")
	if err != nil {
		t.Fatal(err)
	}
	app, err := s.Authenticate(ctx, secret)
	if err != nil || app.Enabled {
		t.Fatal("new applications must start disabled", app, err)
	}
	if _, err := s.Register(ctx, app.AppID, registration()); err == nil {
		t.Fatal("application self-approved")
	}
	if err := s.SetEnabled(ctx, app.AppID, 0, true, "operator", "approve application"); err != nil {
		t.Fatal(err)
	}
	first, err := s.Register(ctx, app.AppID, registration())
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := s.RotateCredential(ctx, app.AppID, "operator", "replace credential")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Authenticate(ctx, secret)
	requireCode(t, err, 401, "INTEGRATION_AUTH_REQUIRED")
	app, err = s.Authenticate(ctx, rotated)
	if err != nil || app.AppID != "second" || app.MaxAgents != 1 {
		t.Fatal("rotation changed application", app, err)
	}
	again, err := s.Register(ctx, app.AppID, registration())
	if err != nil || again.Agent.AgentID != first.Agent.AgentID {
		t.Fatal("rotation changed managed identity", again, err)
	}
	_, err = s.Register(ctx, "partner", registration())
	if err != nil {
		t.Fatal("separate app cannot reuse an external identifier", err)
	}
}
