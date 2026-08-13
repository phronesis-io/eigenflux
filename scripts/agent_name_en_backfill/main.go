// Command agent_name_en_backfill generates English display names for agents.
//
// Dry-run all missing names:
//
//	go run ./scripts/agent_name_en_backfill --all --dry-run
//
// Backfill all missing names:
//
//	go run ./scripts/agent_name_en_backfill --all --workers 8 --pause 100ms
//
// Existing values are retained by default, making the command resumable. Use
// --force only when intentionally regenerating already-populated names.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"eigenflux_server/pipeline/llm"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
)

type options struct {
	all      bool
	agentIDs []int64
	limit    int
	workers  int
	pause    time.Duration
	dryRun   bool
	force    bool
}

type targetAgent struct {
	AgentID     int64  `gorm:"column:agent_id"`
	AgentName   string `gorm:"column:agent_name"`
	EnglishName string `gorm:"column:agent_name_en"`
}

type result struct {
	updated bool
	err     error
}

func main() {
	opts, err := parseOptions()
	if err != nil {
		log.Fatal(err)
	}

	cfg := config.Load()
	db.Init(cfg.PgDSN)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	targets, err := loadTargets(ctx, opts)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("agent English-name backfill targets=%d dry_run=%t force=%t workers=%d pause=%s", len(targets), opts.dryRun, opts.force, opts.workers, opts.pause)
	if len(targets) == 0 || opts.dryRun {
		return
	}
	if strings.TrimSpace(cfg.LLMApiKey) == "" {
		log.Fatal("LLM_API_KEY is required")
	}

	client := llm.NewClient(cfg, nil).WithModel(cfg.LLMTranslateModel).WithReasoningOff()
	updated, skipped, failed := processTargets(ctx, targets, client, opts)
	log.Printf("agent English-name backfill finished updated=%d skipped=%d failed=%d total=%d", updated, skipped, failed, len(targets))
	if failed > 0 {
		os.Exit(1)
	}
}

func parseOptions() (options, error) {
	all := flag.Bool("all", false, "scan every agent in scope")
	agentIDsRaw := flag.String("agent-ids", "", "comma-separated agent IDs")
	limit := flag.Int("limit", 0, "maximum number of agents to scan")
	workers := flag.Int("workers", 8, "number of concurrent LLM workers")
	pause := flag.Duration("pause", 100*time.Millisecond, "per-worker pause after each LLM call")
	dryRun := flag.Bool("dry-run", false, "report targets without calling the model or updating PostgreSQL")
	force := flag.Bool("force", false, "regenerate non-empty agent_name_en values")
	flag.Parse()

	agentIDs, err := parseAgentIDs(*agentIDsRaw)
	if err != nil {
		return options{}, err
	}
	opts := options{all: *all, agentIDs: agentIDs, limit: *limit, workers: *workers, pause: *pause, dryRun: *dryRun, force: *force}
	if !opts.all && len(opts.agentIDs) == 0 {
		return options{}, errors.New("set --all or provide --agent-ids")
	}
	if opts.limit < 0 {
		return options{}, errors.New("limit must be >= 0")
	}
	if opts.workers <= 0 {
		return options{}, errors.New("workers must be > 0")
	}
	if opts.pause < 0 {
		return options{}, errors.New("pause must be >= 0")
	}
	return opts, nil
}

func parseAgentIDs(raw string) ([]int64, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	seen := make(map[int64]struct{})
	ids := make([]int64, 0)
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("invalid agent ID %q", part)
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids, nil
}

func loadTargets(ctx context.Context, opts options) ([]targetAgent, error) {
	query := db.DB.WithContext(ctx).Table("agents").
		Select("agent_id, agent_name, agent_name_en").
		Where("agent_name <> ''").
		Order("agent_id ASC")
	if !opts.force {
		query = query.Where("agent_name_en = ''")
	}
	if len(opts.agentIDs) > 0 {
		query = query.Where("agent_id IN ?", opts.agentIDs)
	}
	if opts.limit > 0 {
		query = query.Limit(opts.limit)
	}
	var targets []targetAgent
	if err := query.Scan(&targets).Error; err != nil {
		return nil, fmt.Errorf("load targets: %w", err)
	}
	return targets, nil
}

func processTargets(ctx context.Context, targets []targetAgent, client *llm.Client, opts options) (updated, skipped, failed int) {
	jobs := make(chan targetAgent)
	results := make(chan result, len(targets))
	var workers sync.WaitGroup

	for i := 0; i < opts.workers; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for target := range jobs {
				results <- processTarget(ctx, target, client, opts)
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, target := range targets {
			select {
			case jobs <- target:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		workers.Wait()
		close(results)
	}()

	for res := range results {
		switch {
		case res.err != nil:
			failed++
			log.Printf("agent English-name backfill error: %v", res.err)
		case res.updated:
			updated++
		default:
			skipped++
		}
	}
	return updated, skipped, failed
}

func processTarget(ctx context.Context, target targetAgent, client *llm.Client, opts options) result {
	englishName, err := client.TranslateAgentNameToEnglish(ctx, target.AgentName)
	if opts.pause > 0 {
		time.Sleep(opts.pause)
	}
	if err != nil {
		return result{err: fmt.Errorf("agent_id=%d translate: %w", target.AgentID, err)}
	}

	query := db.DB.WithContext(ctx).Model(&targetAgent{}).
		Table("agents").
		Where("agent_id = ? AND agent_name = ?", target.AgentID, target.AgentName)
	if !opts.force {
		query = query.Where("agent_name_en = ''")
	}
	update := query.Update("agent_name_en", englishName)
	if update.Error != nil {
		return result{err: fmt.Errorf("agent_id=%d update: %w", target.AgentID, update.Error)}
	}
	return result{updated: update.RowsAffected == 1}
}
