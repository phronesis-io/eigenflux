package dal

import (
	"database/sql"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
)

// probeDB skips the test if Postgres is not reachable; otherwise it ensures
// db.DB is initialised exactly once. db.Init calls os.Exit on failure, hence
// the explicit probe before calling it.
func probeDB(t *testing.T) {
	t.Helper()
	cfg := config.Load()
	probe, err := sql.Open("pgx", cfg.PgDSN)
	if err != nil {
		t.Skipf("db not available: %v", err)
	}
	if err := probe.Ping(); err != nil {
		probe.Close()
		t.Skipf("db not available: %v", err)
	}
	probe.Close()
	if db.DB == nil {
		db.Init(cfg.PgDSN)
	}
}

func TestCreateAndGetServiceWithEnrichment(t *testing.T) {
	probeDB(t)

	svc := &TradingService{
		ServiceID:          9990001,
		SellerAgentID:      1,
		Title:              "t",
		CapabilityDesc:     "c",
		CallSpecText:       "s",
		AmountAtomic:       1000,
		Asset:              "USDC",
		DeliveryDeadlineMs: 60000,
		Status:             1,
		CreatedAt:          1,
		UpdatedAt:          1,
		CapabilityTags:     []string{"translate:es-zh", "language:spanish"},
		UseCases:           "Use when you need Spanish-to-Chinese translation before downstream processing.",
		CanonicalInputs:    `[{"name":"text","type":"string"}]`,
		CanonicalOutputs:   `[{"name":"translated","type":"string"}]`,
		EnrichmentVersion:  1,
	}
	db.DB.Exec("DELETE FROM trading_services WHERE service_id = ?", svc.ServiceID)
	if err := CreateService(db.DB, svc); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() {
		db.DB.Exec("DELETE FROM trading_services WHERE service_id = ?", svc.ServiceID)
	})

	got, err := GetService(db.DB, svc.ServiceID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.CapabilityTags) != 2 || got.CapabilityTags[0] != "translate:es-zh" {
		t.Errorf("CapabilityTags round-trip failed: %#v", got.CapabilityTags)
	}
	if got.UseCases == "" {
		t.Errorf("UseCases empty after round-trip")
	}
	if got.EnrichmentVersion != 1 {
		t.Errorf("EnrichmentVersion = %d, want 1", got.EnrichmentVersion)
	}
}

func TestUpdateServiceEnrichment(t *testing.T) {
	probeDB(t)

	svc := &TradingService{
		ServiceID:          9990002,
		SellerAgentID:      1,
		Title:              "t",
		CapabilityDesc:     "c",
		CallSpecText:       "s",
		AmountAtomic:       1000,
		Asset:              "USDC",
		DeliveryDeadlineMs: 60000,
		Status:             1,
		CreatedAt:          1,
		UpdatedAt:          1,
	}
	db.DB.Exec("DELETE FROM trading_services WHERE service_id = ?", svc.ServiceID)
	if err := CreateService(db.DB, svc); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() {
		db.DB.Exec("DELETE FROM trading_services WHERE service_id = ?", svc.ServiceID)
	})

	err := UpdateServiceEnrichment(db.DB, svc.ServiceID,
		[]string{"summarize:long-text"},
		"Use when you need long-text summarization.",
		`[]`, `[]`, 1)
	if err != nil {
		t.Fatalf("update: %v", err)
	}

	got, err := GetService(db.DB, svc.ServiceID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.CapabilityTags) != 1 || got.CapabilityTags[0] != "summarize:long-text" {
		t.Errorf("update did not persist tags: %#v", got.CapabilityTags)
	}
	if got.EnrichmentVersion != 1 {
		t.Errorf("update did not bump version: %d", got.EnrichmentVersion)
	}
}
