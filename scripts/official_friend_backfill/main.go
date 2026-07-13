// Command official_friend_backfill one-shot backfills the official-account
// friendship for existing users. New users get friended by the
// OfficialWelcomeConsumer on profile completion, but users who onboarded before
// the welcome was opened to everyone were never friended — this walks them and
// builds the relation.
//
// It only builds the friend relation; it does NOT send a welcome PM (existing
// users already know the app; a bulk "welcome" would be spam). Idempotent:
// already-friends are skipped, so it is safe to re-run.
//
//	go run ./scripts/official_friend_backfill --dry-run   # report target count only
//	go run ./scripts/official_friend_backfill             # backfill everyone
//	go run ./scripts/official_friend_backfill --limit 500 # cap for a first pass
package main

import (
	"context"
	"flag"
	"log"
	"sync"
	"sync/atomic"

	"gorm.io/gorm"

	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/mq"
	pmdal "eigenflux_server/rpc/pm/dal"
	"eigenflux_server/rpc/pm/relations"
	profiledal "eigenflux_server/rpc/profile/dal"
)

// Matches OfficialWelcomeConsumer's officialWelcomeRemark.
const officialFriendRemark = "EigenFlux 官方"

type result int

const (
	resultCreated result = iota
	resultSkipped
	resultFailed
)

func main() {
	cfg := config.Load()

	dryRun := flag.Bool("dry-run", false, "report target count only, no writes")
	workers := flag.Int("workers", 8, "concurrent workers")
	limit := flag.Int("limit", 0, "max users to process (0 = all)")
	flag.Parse()

	db.Init(cfg.PgDSN)
	mq.Init(cfg.RedisAddr, cfg.RedisPassword)
	ctx := context.Background()

	official, err := profiledal.GetAgentByEmail(db.DB, cfg.OfficialAgentEmail)
	if err != nil || official == nil {
		log.Fatalf("official account not found (email=%s): %v", cfg.OfficialAgentEmail, err)
	}
	officialID := official.AgentID
	log.Printf("official account agent_id=%d email=%s", officialID, cfg.OfficialAgentEmail)

	targets, err := loadTargets(officialID, *limit)
	if err != nil {
		log.Fatalf("load targets: %v", err)
	}
	log.Printf("backfill targets: %d users (profile-complete, not yet official friends, not blocking official)", len(targets))
	if *dryRun {
		log.Println("dry-run: no writes")
		return
	}
	if len(targets) == 0 {
		return
	}

	var created, skipped, failed int64
	work := make(chan int64)
	var wg sync.WaitGroup
	for i := 0; i < *workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for uid := range work {
				switch ensureOfficialFriend(ctx, officialID, uid) {
				case resultCreated:
					if n := atomic.AddInt64(&created, 1); n%200 == 0 {
						log.Printf("progress: created=%d skipped=%d failed=%d",
							n, atomic.LoadInt64(&skipped), atomic.LoadInt64(&failed))
					}
				case resultSkipped:
					atomic.AddInt64(&skipped, 1)
				case resultFailed:
					atomic.AddInt64(&failed, 1)
				}
			}
		}()
	}
	for _, uid := range targets {
		work <- uid
	}
	close(work)
	wg.Wait()

	log.Printf("backfill done: created=%d skipped(already friend)=%d failed=%d total=%d",
		created, skipped, failed, len(targets))
}

// ensureOfficialFriend mirrors OfficialWelcomeConsumer.ensureFriendship: build
// the symmetric relation in a locked transaction (idempotent), then invalidate
// both friend caches so PMService sees the new relation. No welcome PM is sent.
func ensureOfficialFriend(ctx context.Context, officialID, userID int64) result {
	created := false
	err := db.DB.Transaction(func(tx *gorm.DB) error {
		if err := pmdal.LockRelationPair(tx, officialID, userID); err != nil {
			return err
		}
		isFriend, err := pmdal.IsFriend(tx, officialID, userID)
		if err != nil {
			return err
		}
		if isFriend {
			return nil
		}
		if err := pmdal.CreateFriendRelation(tx, officialID, userID, officialFriendRemark, ""); err != nil {
			return err
		}
		created = true
		return nil
	})
	if err != nil {
		log.Printf("agent_id=%d failed: %v", userID, err)
		return resultFailed
	}
	if !created {
		return resultSkipped
	}
	_ = relations.InvalidateFriendCache(ctx, mq.RDB, officialID)
	_ = relations.InvalidateFriendCache(ctx, mq.RDB, userID)
	return resultCreated
}

// loadTargets returns agent_ids that should be friended: profile-complete,
// non-official users who are not already friends with the official account and
// have not blocked it.
func loadTargets(officialID int64, limit int) ([]int64, error) {
	var ids []int64
	q := db.DB.Raw(`
		SELECT a.agent_id
		  FROM agents a
		 WHERE a.is_official = FALSE
		   AND a.profile_completed_at IS NOT NULL
		   AND NOT EXISTS (
		         SELECT 1 FROM user_relations ur
		          WHERE ur.from_uid = a.agent_id AND ur.to_uid = ? AND ur.rel_type = ?)
		   AND NOT EXISTS (
		         SELECT 1 FROM user_relations ur
		          WHERE ur.from_uid = a.agent_id AND ur.to_uid = ? AND ur.rel_type = ?)
		 ORDER BY a.agent_id ASC`,
		officialID, pmdal.RelTypeFriend, officialID, pmdal.RelTypeBlock)
	if err := q.Scan(&ids).Error; err != nil {
		return nil, err
	}
	if limit > 0 && len(ids) > limit {
		ids = ids[:limit]
	}
	return ids, nil
}
