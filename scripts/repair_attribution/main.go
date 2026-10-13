// A bounded, idempotent repair for an already completed, expired broadcast.
// Default is a locked dry run. Apply changes only the three approved text fields
// and schedules the existing completed-item projection repair, never publication.
package main

import (
	"context"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/featureindex"
	itemdal "eigenflux_server/rpc/item/dal"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	glog "gorm.io/gorm/logger"
	"os"
	"strconv"
	"time"
)

type textFields struct {
	Content   string `json:"content"`
	Summary   string `json:"summary"`
	SummaryZh string `json:"summary_zh"`
}
type plan struct {
	ID     int64      `json:"item_id,string"`
	Author int64      `json:"author_id,string"`
	Group  int64      `json:"group_id,string"`
	URL    string     `json:"url"`
	Expiry string     `json:"expire_time"`
	Before textFields `json:"before"`
	After  textFields `json:"after"`
}

func validate(p plan, r itemdal.RawItem, s itemdal.ProcessedItem, now time.Time) (bool, error) {
	if p.ID <= 0 || p.Author <= 0 || p.Group <= 0 || p.After.Content == "" || p.After.Summary == "" || p.After.SummaryZh == "" {
		return false, errors.New("incomplete repair plan")
	}
	expiry := featureindex.ParseExpiry(s.ExpireTime)
	if r.ItemID != p.ID || s.ItemID != p.ID || r.AuthorAgentID != p.Author || r.RawURL != p.URL || s.GroupID != p.Group || s.Status != itemdal.StatusCompleted || s.ExpireTime != p.Expiry || expiry == nil || !expiry.Before(now) {
		return false, errors.New("identity/lifecycle precondition changed")
	}
	actual := textFields{r.RawContent, s.Summary, s.SummaryZh}
	if actual == p.After {
		return false, nil
	}
	if actual != p.Before {
		return false, errors.New("text precondition changed")
	}
	return true, nil
}
func main() {
	path := flag.String("plan", "", "reviewed JSON repair plan")
	apply := flag.Bool("apply", false, "apply exact text repair and refresh completed-item projections")
	flag.Parse()
	b, err := os.ReadFile(*path)
	check(err)
	var p plan
	check(json.Unmarshal(b, &p))
	cfg := config.Load()
	db, err := gorm.Open(postgres.New(postgres.Config{DSN: cfg.PgDSN, PreferSimpleProtocol: true}), &gorm.Config{DisableAutomaticPing: true, Logger: glog.Default.LogMode(glog.Silent)})
	check(err)
	sql, err := db.DB()
	check(err)
	defer sql.Close()
	sql.SetMaxOpenConns(1)
	sql.SetMaxIdleConns(0)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	changed := false
	check(db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		changed, err = repairText(tx, p, *apply)
		return err
	}))
	if *apply {
		rdb := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr, Password: cfg.RedisPassword})
		defer rdb.Close()
		// Repeating after interruption is safe: StatusCompleted takes the projection-
		// only path, retaining identity, group, expiry, classification and delivery state.
		_, err = rdb.XAdd(ctx, &redis.XAddArgs{Stream: "stream:item:publish", Values: map[string]any{"item_id": strconv.FormatInt(p.ID, 10)}}).Result()
		check(err)
	}
	fmt.Printf("item %d: plan validated; applied=%t changed=%t projection_repair_queued=%t\n", p.ID, *apply, changed, *apply)
}

// Each table read starts from the transaction so model/table state cannot leak.
func repairText(tx *gorm.DB, p plan, apply bool) (bool, error) {
	var r itemdal.RawItem
	var s itemdal.ProcessedItem
	readRecord := func(record any) error {
		query := tx
		if apply {
			query = query.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		return query.First(record, "item_id = ?", p.ID).Error
	}
	if err := readRecord(&r); err != nil {
		return false, err
	}
	if err := readRecord(&s); err != nil {
		return false, err
	}
	need, e := validate(p, r, s, time.Now())
	if e != nil {
		return false, e
	}
	if !apply || !need {
		return false, nil
	}
	if e = tx.Model(&r).Update("raw_content", p.After.Content).Error; e != nil {
		return false, e
	}
	if e = tx.Model(&s).Updates(map[string]any{"summary": p.After.Summary, "summary_zh": p.After.SummaryZh, "updated_at": time.Now().UnixMilli()}).Error; e != nil {
		return false, e
	}
	return true, nil
}

func check(e error) {
	if e != nil {
		fmt.Fprintln(os.Stderr, "repair failed:", e)
		os.Exit(1)
	}
}
