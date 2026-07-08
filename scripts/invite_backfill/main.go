// Command invite_backfill provisions the stable KOL invite code for every
// existing agent that doesn't have one yet (new agents get theirs at
// registration; pre-feature accounts also get one lazily on their first
// dashboard load). Internal fleet accounts (bot/pgc emails) are skipped — they
// never invite anyone and would only pollute the KOL leaderboard.
//
// Idempotent and re-runnable: agents that already own a code are not touched.
//
//	go run ./scripts/invite_backfill --dry-run  # report only
//	go run ./scripts/invite_backfill            # create missing codes
package main

import (
	"flag"
	"log"

	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/invite"
)

func main() {
	dryRun := flag.Bool("dry-run", false, "report how many codes would be created without writing")
	flag.Parse()

	cfg := config.Load()
	db.Init(cfg.PgDSN)

	var agentIDs []int64
	err := db.DB.Raw(`
		SELECT a.agent_id FROM agents a
		 WHERE lower(a.email) NOT LIKE '%bot.eigenflux%'
		   AND lower(a.email) NOT LIKE '%pgc.eigenflux%'
		   AND NOT EXISTS (
		         SELECT 1 FROM invite_codes ic
		          WHERE ic.kind = 'kol' AND ic.agent_id = a.agent_id)
		 ORDER BY a.agent_id`).Scan(&agentIDs).Error
	if err != nil {
		log.Fatalf("list agents missing invite codes: %v", err)
	}
	log.Printf("%d agents missing a KOL invite code", len(agentIDs))
	if *dryRun {
		log.Println("dry-run: no write")
		return
	}

	created, failed := 0, 0
	for _, id := range agentIDs {
		if _, err := invite.EnsureForAgent(db.DB, id); err != nil {
			failed++
			log.Printf("agent %d: %v", id, err)
			continue
		}
		created++
	}
	log.Printf("done: %d created, %d failed", created, failed)
	if failed > 0 {
		log.Fatal("some agents failed — re-run to retry (idempotent)")
	}
}
