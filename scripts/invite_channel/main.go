// Command invite_channel manages stable channel invite codes (渠道码) and looks
// up KOL codes, for ops running on the prod host (same pattern as
// scripts/official_register: config + direct DB).
//
//	go run ./scripts/invite_channel --name redskills --note "official slot"  # create (idempotent by name)
//	go run ./scripts/invite_channel --list                                   # all channel codes + funnel counts
//	go run ./scripts/invite_channel --find kol@example.com                   # an agent's KOL code (creates if missing)
//
// Codes are always system-generated (EFI-xxxxxx); custom vanity codes are
// deliberately unsupported. Creating the same --name twice returns the existing
// code so one channel can never split into two.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/invite"
)

func installBase() string {
	if v := strings.TrimRight(os.Getenv("INSTALL_BASE_URL"), "/"); v != "" {
		return v
	}
	return "https://www.eigenflux.net"
}

func printLinks(code string) {
	base := installBase()
	fmt.Printf("  landing page link : %s/install?ic=%s\n", base, code)
	fmt.Printf("  agent direct link : %s/r/%s\n", base, code)
}

func main() {
	name := flag.String("name", "", "channel name to create/get a code for (e.g. redskills)")
	note := flag.String("note", "", "free-form note stored with a newly created channel code")
	list := flag.Bool("list", false, "list all channel codes with funnel counts")
	find := flag.String("find", "", "agent email — print (creating if missing) that agent's KOL code")
	flag.Parse()

	cfg := config.Load()
	db.Init(cfg.PgDSN)

	switch {
	case *list:
		listChannels()
	case *find != "":
		findKOL(*find)
	case *name != "":
		c, created, err := invite.EnsureChannel(db.DB, *name, *note)
		if err != nil {
			log.Fatalf("ensure channel code: %v", err)
		}
		verb := "existing"
		if created {
			verb = "created"
		}
		fmt.Printf("%s channel code\n  code: %s   kind: %s   name: %s   note: %s\n", verb, c.Code, c.Kind, c.Name, c.Note)
		printLinks(c.Code)
	default:
		flag.Usage()
		os.Exit(2)
	}
}

type channelRow struct {
	Code       string
	Name       string
	Note       string
	Landings   int64
	Installs   int64
	Registered int64
}

func listChannels() {
	var rows []channelRow
	err := db.DB.Raw(`
		SELECT ic.code, ic.name, ic.note,
		       count(t.token)                                        AS landings,
		       count(t.token) FILTER (WHERE t.status = 'installed')  AS installs,
		       (SELECT count(*) FROM agents a WHERE a.invited_by_code = ic.code) AS registered
		  FROM invite_codes ic
		  LEFT JOIN install_tokens t ON t.invite_code = ic.code
		 WHERE ic.kind = 'channel'
		 GROUP BY ic.code, ic.name, ic.note
		 ORDER BY installs DESC, ic.code`).Scan(&rows).Error
	if err != nil {
		log.Fatalf("list channel codes: %v", err)
	}
	if len(rows) == 0 {
		fmt.Println("no channel codes yet — create one with --name <channel>")
		return
	}
	fmt.Printf("%-12s %-20s %8s %8s %10s  %s\n", "CODE", "NAME", "LANDING", "INSTALL", "REGISTERED", "NOTE")
	for _, r := range rows {
		fmt.Printf("%-12s %-20s %8d %8d %10d  %s\n", r.Code, r.Name, r.Landings, r.Installs, r.Registered, r.Note)
	}
}

func findKOL(email string) {
	var agentID int64
	err := db.DB.Raw(`SELECT agent_id FROM agents WHERE email = ?`, strings.ToLower(strings.TrimSpace(email))).
		Scan(&agentID).Error
	if err != nil {
		log.Fatalf("lookup agent by email: %v", err)
	}
	if agentID == 0 {
		log.Fatalf("no agent found for email %s", email)
	}
	c, err := invite.EnsureForAgent(db.DB, agentID)
	if err != nil {
		log.Fatalf("ensure KOL code: %v", err)
	}
	fmt.Printf("KOL code for %s (agent %d)\n  code: %s\n", email, agentID, c.Code)
	printLinks(c.Code)
}
