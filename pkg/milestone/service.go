package milestone

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"text/template"
	"time"

	milestonedal "eigenflux_server/pkg/milestone/dal"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

const DefaultSummaryMaxRunes = 20

type Service struct {
	db              *gorm.DB
	ruleCache       *RuleCache
	notifications   *NotificationStore
	idGenerator     IDGenerator
	nowMillis       func() int64
	summaryMaxRunes int
}

type Option func(*Service)

func WithRuleCacheTTL(ttl time.Duration) Option {
	return func(s *Service) {
		s.ruleCache = NewRuleCache(s.db, ttl)
	}
}

func WithRuleCacheTTLSeconds(seconds int) Option {
	return func(s *Service) {
		if seconds <= 0 {
			return
		}
		s.ruleCache = NewRuleCache(s.db, time.Duration(seconds)*time.Second)
	}
}

func WithNotificationTTL(ttl time.Duration) Option {
	return func(s *Service) {
		s.notifications = NewNotificationStore(s.notifications.rdb, ttl)
	}
}

func WithNowMillis(now func() int64) Option {
	return func(s *Service) {
		s.nowMillis = now
	}
}

func WithSummaryMaxRunes(max int) Option {
	return func(s *Service) {
		s.summaryMaxRunes = max
	}
}

func WithRuleCache(ruleCache *RuleCache) Option {
	return func(s *Service) {
		s.ruleCache = ruleCache
	}
}

func WithNotificationStore(store *NotificationStore) Option {
	return func(s *Service) {
		s.notifications = store
	}
}

func NewService(db *gorm.DB, rdb *redis.Client, idGenerator IDGenerator, opts ...Option) (*Service, error) {
	if db == nil {
		return nil, ErrNilDB
	}
	if rdb == nil {
		return nil, ErrNilRedisClient
	}
	if idGenerator == nil {
		return nil, ErrNilIDGenerator
	}

	svc := &Service{
		db:              db,
		ruleCache:       NewRuleCache(db, DefaultRuleCacheTTL),
		notifications:   NewNotificationStore(rdb, DefaultNotificationTTL),
		idGenerator:     idGenerator,
		nowMillis:       func() int64 { return time.Now().UnixMilli() },
		summaryMaxRunes: DefaultSummaryMaxRunes,
	}
	for _, opt := range opts {
		opt(svc)
	}
	return svc, nil
}

func (s *Service) Check(ctx context.Context, itemID int64, metricKey string, currentCount int64) error {
	if !isValidMetricKey(metricKey) {
		return ErrInvalidMetricKey
	}

	rules, err := s.ruleCache.GetEnabledRules(ctx, metricKey)
	if err != nil {
		return fmt.Errorf("load milestone rules: %w", err)
	}
	if len(rules) == 0 {
		return nil
	}

	matchedRules := make([]milestonedal.MilestoneRule, 0, len(rules))
	for _, rule := range rules {
		if currentCount >= rule.Threshold {
			matchedRules = append(matchedRules, rule)
		}
	}
	if len(matchedRules) == 0 {
		return nil
	}

	itemCtx, err := milestonedal.GetItemContext(ctx, s.db, itemID)
	if err != nil {
		return fmt.Errorf("load milestone item context: %w", err)
	}

	templateData := TemplateData{
		ItemID:       itemID,
		CounterValue: currentCount,
		ItemSummary:  prepareItemSummary(itemCtx.ItemSummary, s.summaryMaxRunes),
	}

	for _, rule := range matchedRules {
		now := s.nowMillis()
		eventID, err := s.idGenerator.NextID()
		if err != nil {
			return fmt.Errorf("generate milestone event id: %w", err)
		}

		templateData.Threshold = rule.Threshold
		content, err := renderContent(rule.ContentTemplate, templateData)
		if err != nil {
			return fmt.Errorf("render milestone content for rule %d: %w", rule.RuleID, err)
		}

		event := &milestonedal.MilestoneEvent{
			EventID:             eventID,
			ItemID:              itemID,
			AuthorAgentID:       itemCtx.AuthorAgentID,
			RuleID:              rule.RuleID,
			MetricKey:           metricKey,
			Threshold:           rule.Threshold,
			CounterValue:        currentCount,
			NotificationContent: content,
			NotificationStatus:  milestonedal.NotificationStatusPending,
			QueuedAt:            now,
			NotifiedAt:          0,
			TriggeredAt:         now,
		}
		inserted, err := milestonedal.InsertEventIfAbsent(ctx, s.db, event)
		if err != nil {
			return fmt.Errorf("insert milestone event: %w", err)
		}
		if !inserted {
			continue
		}

		if err := s.notifications.Put(ctx, itemCtx.AuthorAgentID, NotificationFromEvent(*event)); err != nil {
			return err
		}
	}

	return nil
}

func (s *Service) RecoverPendingNotifications(ctx context.Context, limit int) (int, error) {
	batchSize := limit
	if batchSize <= 0 {
		batchSize = 100
	}

	var (
		recovered     int
		afterQueuedAt int64
		afterEventID  int64
	)
	for {
		events, err := milestonedal.ListPendingEventsAfter(ctx, s.db, afterQueuedAt, afterEventID, batchSize)
		if err != nil {
			return recovered, fmt.Errorf("list pending milestone events: %w", err)
		}
		if len(events) == 0 {
			return recovered, nil
		}

		for _, event := range events {
			if err := s.notifications.Put(ctx, event.AuthorAgentID, NotificationFromEvent(event)); err != nil {
				return recovered, err
			}
			recovered++
		}

		lastEvent := events[len(events)-1]
		afterQueuedAt = lastEvent.QueuedAt
		afterEventID = lastEvent.EventID
		if len(events) < batchSize {
			return recovered, nil
		}
	}
}

func (s *Service) InvalidateRules(metricKey string) {
	if s == nil || s.ruleCache == nil {
		return
	}
	s.ruleCache.Invalidate(metricKey)
}

func (s *Service) InvalidateAllRules() {
	if s == nil || s.ruleCache == nil {
		return
	}
	s.ruleCache.InvalidateAll()
}

func renderContent(contentTemplate string, data TemplateData) (string, error) {
	tpl, err := template.New("milestone_content").Option("missingkey=error").Parse(contentTemplate)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if err := tpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func prepareItemSummary(summary string, maxRunes int) string {
	summary = strings.Join(strings.Fields(summary), " ")
	if maxRunes <= 0 {
		return summary
	}

	runes := []rune(summary)
	if len(runes) <= maxRunes {
		return summary
	}
	return string(runes[:maxRunes])
}
