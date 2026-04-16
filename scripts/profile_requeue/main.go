package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"eigenflux_server/pipeline/llm"
	"eigenflux_server/pkg/cache"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/mq"
	profileDal "eigenflux_server/rpc/profile/dal"
)

type options struct {
	all           bool
	agentIDs      []int64
	statuses      []int16
	limit         int
	workers       int
	pause         time.Duration
	dryRun        bool
	updateCountry bool
}

type targetAgent struct {
	AgentID int64  `gorm:"column:agent_id"`
	Status  int16  `gorm:"column:status"`
	Bio     string `gorm:"column:bio"`
	Country string `gorm:"column:country"`
}

type backfillResult struct {
	AgentID int64
	Err     error
}

func main() {
	opts, err := parseOptions()
	if err != nil {
		log.Fatal(err)
	}

	cfg := config.Load()
	db.Init(cfg.PgDSN)
	mq.Init(cfg.RedisAddr, cfg.RedisPassword)
	prompts, err := llm.LoadDefaultPrompts()
	if err != nil {
		log.Fatalf("load prompts: %v", err)
	}
	if err := llm.ValidateAllPrompts(prompts); err != nil {
		log.Fatalf("validate prompts: %v", err)
	}
	llmClient := llm.NewClient(cfg, prompts)
	profileCache := cache.NewProfileCache(mq.RDB, time.Duration(cfg.ProfileCacheTTL)*time.Second)

	ctx := context.Background()
	targets, err := loadTargets(ctx, opts)
	if err != nil {
		log.Fatal(err)
	}
	if len(targets) == 0 {
		log.Println("no profile targets matched")
		return
	}

	log.Printf(
		"profile keyword backfill targets=%d dry_run=%t workers=%d pause=%s update_country=%t",
		len(targets), opts.dryRun, opts.workers, opts.pause, opts.updateCountry,
	)
	log.Printf("sample agent_ids=%s", summarizeIDs(targets, 20))

	if opts.dryRun {
		return
	}

	success, failed := processTargets(ctx, targets, llmClient, profileCache, opts)
	log.Printf("profile keyword backfill finished success=%d failed=%d", success, failed)
}

func parseOptions() (options, error) {
	var (
		all           = flag.Bool("all", false, "backfill every profile that has a non-empty bio")
		agentIDsRaw   = flag.String("agent-ids", "", "comma-separated agent IDs to backfill")
		statusesRaw   = flag.String("statuses", "3", "comma-separated profile statuses to include, empty means all")
		limit         = flag.Int("limit", 0, "max number of profiles to backfill")
		workers       = flag.Int("workers", 8, "number of concurrent LLM workers")
		pause         = flag.Duration("pause", 100*time.Millisecond, "per-worker sleep after each LLM call")
		dryRun        = flag.Bool("dry-run", false, "print matched profiles without calling the LLM")
		updateCountry = flag.Bool("update-country", false, "also overwrite agent_profiles.country from the LLM result")
	)
	flag.Parse()

	agentIDs, err := parseInt64CSV(*agentIDsRaw)
	if err != nil {
		return options{}, fmt.Errorf("parse agent ids: %w", err)
	}
	statuses, err := parseInt16CSV(*statusesRaw)
	if err != nil {
		return options{}, fmt.Errorf("parse statuses: %w", err)
	}

	opts := options{
		all:           *all,
		agentIDs:      agentIDs,
		statuses:      statuses,
		limit:         *limit,
		workers:       *workers,
		pause:         *pause,
		dryRun:        *dryRun,
		updateCountry: *updateCountry,
	}
	if err := opts.validate(); err != nil {
		return options{}, err
	}
	return opts, nil
}

func (o options) validate() error {
	if !o.all && len(o.agentIDs) == 0 {
		return errors.New("set --all or provide --agent-ids")
	}
	if o.workers <= 0 {
		return errors.New("workers must be > 0")
	}
	if o.limit < 0 {
		return errors.New("limit must be >= 0")
	}
	return nil
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
	slices.Sort(values)
	return values, nil
}

func parseInt16CSV(raw string) ([]int16, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	values := make([]int16, 0, len(parts))
	seen := make(map[int16]struct{}, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		value64, err := strconv.ParseInt(trimmed, 10, 16)
		if err != nil {
			return nil, fmt.Errorf("invalid int16 %q: %w", trimmed, err)
		}
		value := int16(value64)
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		values = append(values, value)
	}
	slices.Sort(values)
	return values, nil
}

func loadTargets(ctx context.Context, opts options) ([]targetAgent, error) {
	query := db.DB.WithContext(ctx).
		Table("agent_profiles AS ap").
		Select("ap.agent_id, ap.status, ap.country, a.bio").
		Joins("JOIN agents AS a ON a.agent_id = ap.agent_id").
		Where("TRIM(COALESCE(a.bio, '')) <> ''").
		Order("ap.agent_id ASC")

	if len(opts.agentIDs) > 0 {
		query = query.Where("ap.agent_id IN ?", opts.agentIDs)
	}
	if len(opts.statuses) > 0 {
		query = query.Where("ap.status IN ?", opts.statuses)
	}
	if opts.limit > 0 {
		query = query.Limit(opts.limit)
	}

	var targets []targetAgent
	if err := query.Find(&targets).Error; err != nil {
		return nil, err
	}
	return targets, nil
}

func processTargets(ctx context.Context, targets []targetAgent, llmClient *llm.Client, profileCache *cache.ProfileCache, opts options) (int, int) {
	workCh := make(chan targetAgent)
	resultCh := make(chan backfillResult, len(targets))

	var wg sync.WaitGroup
	for i := 0; i < opts.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for target := range workCh {
				err := processTarget(ctx, target, llmClient, profileCache, opts)
				resultCh <- backfillResult{AgentID: target.AgentID, Err: err}
				if opts.pause > 0 {
					time.Sleep(opts.pause)
				}
			}
		}()
	}

	go func() {
		for _, target := range targets {
			workCh <- target
		}
		close(workCh)
		wg.Wait()
		close(resultCh)
	}()

	success, failed := 0, 0
	for result := range resultCh {
		if result.Err != nil {
			failed++
			log.Printf("agent_id=%d failed: %v", result.AgentID, result.Err)
			continue
		}
		success++
		if success%50 == 0 || success == len(targets) {
			log.Printf("processed success=%d failed=%d total=%d", success, failed, len(targets))
		}
	}
	return success, failed
}

func processTarget(ctx context.Context, target targetAgent, llmClient *llm.Client, profileCache *cache.ProfileCache, opts options) error {
	if err := profileDal.UpdateAgentProfileStatus(db.DB, target.AgentID, 1); err != nil {
		return fmt.Errorf("set processing status: %w", err)
	}

	keywords, country, err := extractKeywordsWithRetry(ctx, llmClient, target.Bio)
	if err != nil {
		_ = profileDal.UpdateAgentProfileStatus(db.DB, target.AgentID, 2)
		return err
	}

	updates := map[string]interface{}{
		"keywords":   strings.Join(keywords, ","),
		"status":     3,
		"updated_at": time.Now().UnixMilli(),
	}
	cacheCountry := target.Country
	if opts.updateCountry {
		updates["country"] = country
		cacheCountry = country
	}

	if err := db.DB.Model(&profileDal.AgentProfile{}).Where("agent_id = ?", target.AgentID).Updates(updates).Error; err != nil {
		_ = profileDal.UpdateAgentProfileStatus(db.DB, target.AgentID, 2)
		return fmt.Errorf("update profile keywords: %w", err)
	}

	if profileCache != nil {
		if err := profileCache.Set(ctx, buildCachedProfile(target.AgentID, keywords, cacheCountry)); err != nil {
			return fmt.Errorf("refresh profile cache: %w", err)
		}
	}
	return nil
}

func extractKeywordsWithRetry(ctx context.Context, llmClient *llm.Client, bio string) ([]string, string, error) {
	var (
		keywords []string
		country  string
		err      error
	)
	for attempt := 1; attempt <= 3; attempt++ {
		keywords, country, err = llmClient.ExtractKeywords(ctx, bio)
		if err == nil {
			return keywords, country, nil
		}
		time.Sleep(time.Duration(attempt) * time.Second)
	}
	return nil, "", fmt.Errorf("extract keywords failed after retries: %w", err)
}

func buildCachedProfile(agentID int64, keywords []string, country string) *cache.CachedProfile {
	return &cache.CachedProfile{
		AgentID:    agentID,
		Keywords:   keywords,
		Domains:    keywords,
		Geo:        "",
		GeoCountry: country,
	}
}

func summarizeIDs(targets []targetAgent, limit int) string {
	if len(targets) == 0 {
		return ""
	}
	if limit <= 0 || limit > len(targets) {
		limit = len(targets)
	}
	parts := make([]string, 0, limit)
	for _, target := range targets[:limit] {
		parts = append(parts, strconv.FormatInt(target.AgentID, 10))
	}
	if len(targets) > limit {
		return strings.Join(parts, ",") + ",..."
	}
	return strings.Join(parts, ",")
}
