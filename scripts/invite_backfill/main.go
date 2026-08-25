// Command invite_backfill audits legacy personal EFI coverage. New personal
// codes are intentionally not issued; agents.short_id is the public handle.
//
// Idempotent and re-runnable: agents that already own a code are not touched.
//
//	go run ./scripts/invite_backfill --dry-run  # report only
//	go run ./scripts/invite_backfill            # report only; never writes
package main

import (
	"flag"
	"log"

	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
)

func main() {
	dryRun := flag.Bool("dry-run", false, "report legacy personal-code coverage")
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
	log.Printf("%d agents have no legacy KOL invite code; no codes were issued because short_id is the public handle", len(agentIDs))
	_ = *dryRun
}
