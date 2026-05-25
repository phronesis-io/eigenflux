package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/mq"

	"gorm.io/gorm"
)

type options struct {
	agentIDs []int64
	days     int
	limit    int
	dryRun   bool
}

func main() {
	opts, err := parseOptions()
	if err != nil {
		log.Fatal(err)
	}

	cfg := config.Load()
	db.Init(cfg.PgDSN)
	mq.Init(cfg.RedisAddr, cfg.RedisPassword)

	ctx := context.Background()
	ids, err := loadFailedProfiles(ctx, db.DB, opts)
	if err != nil {
		log.Fatalf("load failed profiles: %v", err)
	}
	if len(ids) == 0 {
		log.Println("no failed profiles found")
		return
	}

	log.Printf("found %d failed profiles (days=%d), sample: %s", len(ids), opts.days, summarizeIDs(ids, 20))

	if opts.dryRun {
		return
	}

	success, failed := 0, 0
	for _, id := range ids {
		if err := db.DB.Table("agent_profiles").Where("agent_id = ?", id).Updates(map[string]interface{}{
			"status":     0,
			"updated_at": time.Now().UnixMilli(),
		}).Error; err != nil {
			log.Printf("agent_id=%d reset status failed: %v", id, err)
			failed++
			continue
		}
		if _, err := mq.Publish(ctx, "stream:profile:update", map[string]interface{}{
			"agent_id": strconv.FormatInt(id, 10),
		}); err != nil {
			log.Printf("agent_id=%d publish failed: %v", id, err)
			failed++
			continue
		}
		success++
		if success%50 == 0 {
			log.Printf("progress: published=%d failed=%d total=%d", success, failed, len(ids))
		}
	}
	log.Printf("user requeue finished: published=%d failed=%d", success, failed)
}

func parseOptions() (options, error) {
	var (
		agentIDsRaw = flag.String("agent-ids", "", "comma-separated agent IDs (overrides --days filter)")
		days        = flag.Int("days", 3, "look-back window in days (based on updated_at)")
		limit       = flag.Int("limit", 0, "max profiles to requeue (0 = no limit)")
		dryRun      = flag.Bool("dry-run", false, "print matched profiles without requeueing")
	)
	flag.Parse()

	agentIDs, err := parseInt64CSV(*agentIDsRaw)
	if err != nil {
		return options{}, fmt.Errorf("parse agent ids: %w", err)
	}
	if *days <= 0 {
		return options{}, fmt.Errorf("days must be > 0")
	}

	return options{
		agentIDs: agentIDs,
		days:     *days,
		limit:    *limit,
		dryRun:   *dryRun,
	}, nil
}

func loadFailedProfiles(ctx context.Context, database *gorm.DB, opts options) ([]int64, error) {
	query := database.WithContext(ctx).
		Table("agent_profiles").
		Select("agent_id").
		Where("status = 2")

	if len(opts.agentIDs) > 0 {
		query = query.Where("agent_id IN ?", opts.agentIDs)
	} else {
		cutoffMs := time.Now().AddDate(0, 0, -opts.days).UnixMilli()
		query = query.Where("updated_at >= ?", cutoffMs)
	}

	query = query.Order("agent_id ASC")
	if opts.limit > 0 {
		query = query.Limit(opts.limit)
	}

	var ids []int64
	if err := query.Find(&ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

func parseInt64CSV(raw string) ([]int64, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	values := make([]int64, 0, len(parts))
	seen := make(map[int64]struct{}, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		value, err := strconv.ParseInt(trimmed, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid int64 %q: %w", trimmed, err)
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		values = append(values, value)
	}
	return values, nil
}

func summarizeIDs(ids []int64, limit int) string {
	if len(ids) == 0 {
		return ""
	}
	if limit <= 0 || limit > len(ids) {
		limit = len(ids)
	}
	parts := make([]string, 0, limit)
	for _, id := range ids[:limit] {
		parts = append(parts, strconv.FormatInt(id, 10))
	}
	if len(ids) > limit {
		return strings.Join(parts, ",") + ",..."
	}
	return strings.Join(parts, ",")
}
