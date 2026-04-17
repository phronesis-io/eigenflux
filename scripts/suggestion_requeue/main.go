package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"eigenflux_server/pipeline/llm"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/mq"
	itemDal "eigenflux_server/rpc/item/dal"
)

type options struct {
	all      bool
	itemIDs  []int64
	days     int
	limit    int
	workers  int
	pause    time.Duration
	dryRun   bool
}

type targetItem struct {
	ItemID           int64  `gorm:"column:item_id"`
	RawContent       string `gorm:"column:raw_content"`
	RawNotes         string `gorm:"column:raw_notes"`
	Summary          string `gorm:"column:summary"`
	BroadcastType    string `gorm:"column:broadcast_type"`
	Domains          string `gorm:"column:domains"`
	Keywords         string `gorm:"column:keywords"`
	Geo              string `gorm:"column:geo"`
	Timeliness       string `gorm:"column:timeliness"`
	ExpectedResponse string `gorm:"column:expected_response"`
}

type result struct {
	ItemID int64
	Err    error
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

	ctx := context.Background()
	targets, err := loadTargets(ctx, opts)
	if err != nil {
		log.Fatal(err)
	}
	if len(targets) == 0 {
		log.Println("no targets matched")
		return
	}

	log.Printf(
		"suggestion requeue targets=%d dry_run=%t workers=%d pause=%s",
		len(targets), opts.dryRun, opts.workers, opts.pause,
	)
	log.Printf("sample item_ids=%s", summarizeIDs(targets, 20))

	if opts.dryRun {
		return
	}

	success, failed := processTargets(ctx, targets, llmClient, opts)
	log.Printf("suggestion requeue finished success=%d failed=%d", success, failed)
}

func parseOptions() (options, error) {
	var (
		all        = flag.Bool("all", false, "regenerate suggestions for all matching items")
		itemIDsRaw = flag.String("item-ids", "", "comma-separated item IDs to regenerate")
		days       = flag.Int("days", 7, "look-back window in days (from now)")
		limit      = flag.Int("limit", 0, "max number of items to process (0 = no limit)")
		workers    = flag.Int("workers", 4, "number of concurrent LLM workers")
		pause      = flag.Duration("pause", 200*time.Millisecond, "per-worker sleep after each LLM call")
		dryRun     = flag.Bool("dry-run", false, "print matched items without calling the LLM")
	)
	flag.Parse()

	itemIDs, err := parseInt64CSV(*itemIDsRaw)
	if err != nil {
		return options{}, fmt.Errorf("parse item ids: %w", err)
	}

	opts := options{
		all:     *all,
		itemIDs: itemIDs,
		days:    *days,
		limit:   *limit,
		workers: *workers,
		pause:   *pause,
		dryRun:  *dryRun,
	}
	if err := opts.validate(); err != nil {
		return options{}, err
	}
	return opts, nil
}

func (o options) validate() error {
	if !o.all && len(o.itemIDs) == 0 {
		return errors.New("set --all or provide --item-ids")
	}
	if o.workers <= 0 {
		return errors.New("workers must be > 0")
	}
	if o.limit < 0 {
		return errors.New("limit must be >= 0")
	}
	if o.days <= 0 {
		return errors.New("days must be > 0")
	}
	return nil
}

func loadTargets(ctx context.Context, opts options) ([]targetItem, error) {
	cutoffMs := time.Now().AddDate(0, 0, -opts.days).UnixMilli()
	nowRFC3339 := time.Now().Format(time.RFC3339)

	query := db.DB.WithContext(ctx).
		Table("processed_items AS pi").
		Select("pi.item_id, ri.raw_content, ri.raw_notes, pi.summary, pi.broadcast_type, pi.domains, pi.keywords, pi.geo, pi.timeliness, pi.expected_response").
		Joins("JOIN raw_items AS ri ON ri.item_id = pi.item_id").
		Where("pi.status = ?", itemDal.StatusCompleted).
		Where("ri.created_at >= ?", cutoffMs).
		Where("pi.expire_time IS NULL OR pi.expire_time >= ?", nowRFC3339).
		Order("pi.item_id ASC")

	if len(opts.itemIDs) > 0 {
		query = query.Where("pi.item_id IN ?", opts.itemIDs)
	}
	if opts.limit > 0 {
		query = query.Limit(opts.limit)
	}

	var targets []targetItem
	if err := query.Find(&targets).Error; err != nil {
		return nil, err
	}
	return targets, nil
}

func processTargets(ctx context.Context, targets []targetItem, llmClient *llm.Client, opts options) (int, int) {
	workCh := make(chan targetItem)
	resultCh := make(chan result, opts.workers*2)

	var wg sync.WaitGroup
	for i := 0; i < opts.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for target := range workCh {
				err := processTarget(ctx, target, llmClient)
				resultCh <- result{ItemID: target.ItemID, Err: err}
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
	for r := range resultCh {
		if r.Err != nil {
			failed++
			log.Printf("item_id=%d failed: %v", r.ItemID, r.Err)
			continue
		}
		success++
		if success%50 == 0 {
			log.Printf("progress success=%d failed=%d total=%d", success, failed, len(targets))
		}
	}
	return success, failed
}

func processTarget(ctx context.Context, target targetItem, llmClient *llm.Client) error {
	input := llm.SuggestActionInput{
		Content:          target.RawContent,
		Notes:            target.RawNotes,
		Summary:          target.Summary,
		BroadcastType:    target.BroadcastType,
		Domains:          splitComma(target.Domains),
		Keywords:         splitComma(target.Keywords),
		Geo:              target.Geo,
		Timeliness:       target.Timeliness,
		ExpectedResponse: target.ExpectedResponse,
	}

	var (
		suggestion string
		err        error
	)
	for attempt := 1; attempt <= 3; attempt++ {
		var res *llm.SuggestActionResult
		res, err = llmClient.SuggestAction(ctx, input)
		if err == nil {
			suggestion = res.Suggestion
			break
		}
		time.Sleep(time.Duration(attempt) * time.Second)
	}
	if err != nil {
		return fmt.Errorf("suggest action failed after retries: %w", err)
	}

	if err := itemDal.UpdateSuggestion(db.DB, target.ItemID, suggestion); err != nil {
		return fmt.Errorf("update suggestion: %w", err)
	}
	return nil
}

func splitComma(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
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

func summarizeIDs(targets []targetItem, limit int) string {
	if len(targets) == 0 {
		return ""
	}
	if limit <= 0 || limit > len(targets) {
		limit = len(targets)
	}
	parts := make([]string, 0, limit)
	for _, t := range targets[:limit] {
		parts = append(parts, strconv.FormatInt(t.ItemID, 10))
	}
	if len(targets) > limit {
		return strings.Join(parts, ",") + ",..."
	}
	return strings.Join(parts, ",")
}
