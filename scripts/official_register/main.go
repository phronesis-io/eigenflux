// Command official_register idempotently provisions the singleton official
// account (the new-user guide / first contact) and flags it is_official=true.
//
// Re-runnable: if the account already exists it is updated in place (no new
// row, agent_id preserved); only first creation needs the etcd-managed ID
// generator. Defaults come from OFFICIAL_AGENT_EMAIL / OFFICIAL_AGENT_NAME.
//
//	go run ./scripts/official_register            # create or update with config defaults
//	go run ./scripts/official_register --dry-run  # report only
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"strings"
	"time"

	"eigenflux_server/pkg/agentcard"
	"eigenflux_server/pkg/agentidentity"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/idgen"
	"eigenflux_server/pkg/metrics"
)

func main() {
	cfg := config.Load()

	email := flag.String("email", cfg.OfficialAgentEmail, "official account email")
	name := flag.String("name", cfg.OfficialAgentName, "official account display name")
	bio := flag.String("bio", cfg.OfficialAgentBio, "official account bio")
	dryRun := flag.Bool("dry-run", false, "report the action without writing")
	flag.Parse()

	if strings.TrimSpace(*email) == "" {
		log.Fatal("email must not be empty")
	}

	db.Init(cfg.PgDSN)
	now := time.Now().UnixMilli()

	var existingID int64
	if err := db.DB.Raw("SELECT agent_id FROM agents WHERE email = ?", *email).Scan(&existingID).Error; err != nil {
		log.Fatalf("lookup official account: %v", err)
	}

	if existingID != 0 {
		log.Printf("official account exists agent_id=%d email=%s — marking is_official=true", existingID, *email)
		if *dryRun {
			log.Println("dry-run: no write")
			return
		}
		if err := db.DB.Exec(`
			UPDATE agents
			   SET is_official = TRUE,
			       agent_name = ?,
			       updated_at = ?,
			       email_verified_at = COALESCE(email_verified_at, ?),
			       profile_completed_at = COALESCE(profile_completed_at, ?)
			 WHERE email = ?`,
			*name, now, now, now, *email).Error; err != nil {
			log.Fatalf("update official account: %v", err)
		}
		log.Printf("official account updated agent_id=%d", existingID)
		return
	}

	log.Printf("official account not found — creating email=%s name=%q", *email, *name)
	if *dryRun {
		log.Println("dry-run: no write")
		return
	}

	// First creation needs a snowflake agent_id from the etcd-managed generator.
	gen := mustIDGen(cfg)
	defer func() { _ = gen.Close(context.Background()) }()
	agentID, err := gen.NextID()
	if err != nil {
		log.Fatalf("generate agent id: %v", err)
	}

	if err := insertOfficialAgent(agentID, *email, *name, *bio, now); err != nil {
		log.Fatalf("create official account: %v", err)
	}
	agentcard.PublishRebuild(context.Background(), agentID, "official_agent_registered")
	log.Printf("official account created agent_id=%d email=%s", agentID, *email)
}

func insertOfficialAgent(agentID int64, email, name, bio string, now int64) error {
	for attempt := 0; attempt < 100; attempt++ {
		shortID, err := agentidentity.GenerateShortID()
		if err != nil {
			return err
		}
		result := db.DB.Exec(`INSERT INTO agents
			(agent_id, short_id, email, agent_name, bio, created_at, updated_at,
			 email_verified_at, profile_completed_at, is_official)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, TRUE)
			ON CONFLICT (short_id) WHERE short_id IS NOT NULL DO NOTHING`,
			agentID, shortID, email, name, bio, now, now, now, now)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 1 {
			return nil
		}
		metrics.AgentShortIDGenerationCollisionTotal.Inc()
	}
	metrics.AgentShortIDGenerationFailureTotal.Inc()
	return errors.New("short-id collision retry budget exhausted")
}

func mustIDGen(cfg *config.Config) *idgen.ManagedGenerator {
	gen, err := idgen.NewManagedGenerator(context.Background(), idgen.ManagedGeneratorConfig{
		Endpoints:      splitCSV(cfg.EtcdAddr),
		WorkerPrefix:   cfg.IDWorkerPrefix,
		ServiceName:    "official-register",
		InstanceID:     cfg.IDInstanceID,
		LeaseTTLSecond: cfg.IDWorkerLeaseTTL,
		EpochMS:        cfg.IDSnowflakeEpoch,
	})
	if err != nil {
		log.Fatalf("init id generator: %v", err)
	}
	return gen
}

func splitCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return []string{"localhost:2379"}
	}
	return out
}
