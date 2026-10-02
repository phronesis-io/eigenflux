package milestone

import (
	"context"
	"eigenflux_server/pkg/cache"
	"time"

	milestonedal "eigenflux_server/pkg/milestone/dal"

	"gorm.io/gorm"
)

const DefaultRuleCacheTTL = 60 * time.Second
const ruleCacheCapacity = 256

type RuleCache struct {
	db  *gorm.DB
	ttl time.Duration
	now func() time.Time

	entries *cache.Local[string, []milestonedal.MilestoneRule]
}

func NewRuleCache(db *gorm.DB, ttl time.Duration) *RuleCache {
	if ttl <= 0 {
		ttl = DefaultRuleCacheTTL
	}
	return &RuleCache{
		db:      db,
		ttl:     ttl,
		now:     time.Now,
		entries: cache.NewLocal[string, []milestonedal.MilestoneRule](ruleCacheCapacity),
	}
}

func (c *RuleCache) GetEnabledRules(ctx context.Context, metricKey string) ([]milestonedal.MilestoneRule, error) {
	now := c.now()

	if rules, ok := c.entries.GetAt(metricKey, now); ok {
		return cloneRules(rules), nil
	}

	rules, err := milestonedal.ListEnabledRulesByMetric(ctx, c.db, metricKey)
	if err != nil {
		return nil, err
	}

	c.entries.PutUntil(metricKey, cloneRules(rules), now.Add(c.ttl))

	return cloneRules(rules), nil
}

func (c *RuleCache) Invalidate(metricKey string) {
	if metricKey == "" {
		return
	}

	c.entries.Delete(metricKey)
}

func (c *RuleCache) InvalidateAll() { c.entries.Clear() }

func cloneRules(rules []milestonedal.MilestoneRule) []milestonedal.MilestoneRule {
	if len(rules) == 0 {
		return nil
	}
	out := make([]milestonedal.MilestoneRule, len(rules))
	copy(out, rules)
	return out
}
