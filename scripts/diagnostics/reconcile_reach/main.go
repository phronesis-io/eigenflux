// Command reconcile_reach raises cumulative consumption to a proven historical
// delivery floor. It does not infer a missing-event delta or unique-agent Reach.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func openDatabase(ctx context.Context, dsn string) (*gorm.DB, error) {
	pool, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	pool.SetMaxOpenConns(1)
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: pool}), &gorm.Config{DisableAutomaticPing: true})
	if err == nil {
		err = pool.PingContext(ctx)
	}
	if err != nil {
		_ = pool.Close()
		return nil, err
	}
	return db, nil
}

type report struct {
	ItemID    int64 `json:"item_id,string"`
	Before    int64 `json:"before"`
	Delivered int64 `json:"historical_deliveries"`
	Agents    int64 `json:"historical_agents"`
	After     int64 `json:"after"`
}

func parseIDs(input string) ([]int64, error) {
	seen := map[int64]bool{}
	for _, part := range strings.Split(input, ",") {
		id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("items must be positive decimal IDs")
		}
		seen[id] = true
	}
	if len(seen) > 100 {
		return nil, fmt.Errorf("at most 100 items per invocation")
	}
	ids := make([]int64, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids, nil
}

func reconcile(ctx context.Context, db *gorm.DB, ids []int64, before int64, apply bool) ([]report, error) {
	if len(ids) == 0 || len(ids) > 100 || before <= 0 {
		return nil, fmt.Errorf("explicit items and historical cutoff required")
	}
	out := []report{}
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if !apply {
			if err := tx.Exec("SET TRANSACTION READ ONLY").Error; err != nil {
				return err
			}
		}
		if err := tx.Exec("SET LOCAL statement_timeout = '15s'").Error; err != nil {
			return err
		}
		if err := tx.Exec("SET LOCAL lock_timeout = '2s'").Error; err != nil {
			return err
		}
		for _, id := range ids {
			var stats struct {
				ItemID        int64
				ConsumedCount int64
			}
			q := tx.Table("item_stats").Select("item_id,consumed_count").Where("item_id = ?", id)
			if apply {
				q = q.Clauses(clause.Locking{Strength: "UPDATE"})
			}
			if err := q.Take(&stats).Error; err != nil {
				return fmt.Errorf("read stats for %d: %w", id, err)
			}
			r := report{ItemID: id, Before: stats.ConsumedCount, After: stats.ConsumedCount}
			var evidence struct{ Delivered, Agents int64 }
			if err := tx.Raw(`SELECT COUNT(DISTINCT (impression_id, agent_id, item_id)) AS delivered,
COUNT(DISTINCT agent_id) AS agents FROM replay_logs
WHERE item_id = ? AND source_kind = 'broadcast' AND delivered IS TRUE
AND impression_id <> '' AND served_at < ?`, id, before).Scan(&evidence).Error; err != nil {
				return err
			}
			r.Delivered, r.Agents = evidence.Delivered, evidence.Agents
			if r.Delivered > r.After {
				r.After = r.Delivered
			}
			if apply && r.After > r.Before {
				if err := tx.Exec(`UPDATE item_stats SET consumed_count = GREATEST(consumed_count, ?), updated_at = ? WHERE item_id = ?`, r.After, time.Now().UnixMilli(), id).Error; err != nil {
					return err
				}
			}
			out = append(out, r)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func main() {
	items := flag.String("items", "", "comma-separated item IDs (1–100)")
	before := flag.Int64("before-ms", 0, "exclusive historical served_at cutoff in epoch milliseconds")
	apply := flag.Bool("apply", false, "apply the delivery floor; default is a read-only preview")
	flag.Parse()
	ids, err := parseIDs(*items)
	if err != nil {
		log.Fatal(err)
	}
	if *before <= 0 || *before > time.Now().Add(-10*time.Minute).UnixMilli() {
		log.Fatal("before-ms must be at least ten minutes in the past")
	}
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		log.Fatal("explicit PG_DSN required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := openDatabase(ctx, dsn)
	if err != nil {
		log.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = pool.Close() }()
	rows, err := reconcile(ctx, db, ids, *before, *apply)
	if err != nil {
		log.Fatal(err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(struct {
		Applied  bool     `json:"applied"`
		BeforeMS int64    `json:"before_ms"`
		Items    []report `json:"items"`
	}{*apply, *before, rows}); err != nil {
		log.Fatal(err)
	}
}
