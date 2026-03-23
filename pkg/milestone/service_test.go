package milestone

import (
	"context"
	"testing"
	"time"

	milestonedal "eigenflux_server/pkg/milestone/dal"
	itemdal "eigenflux_server/rpc/item/dal"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type fakeIDGenerator struct {
	next []int64
	idx  int
}

func (g *fakeIDGenerator) NextID() (int64, error) {
	id := g.next[g.idx]
	g.idx++
	return id, nil
}

func setupMilestoneTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&milestonedal.MilestoneRule{},
		&milestonedal.MilestoneEvent{},
		&itemdal.ProcessedItem{},
		&itemdal.ItemStats{},
	))
	return db
}

func setupMilestoneTestRedis(t *testing.T) (*redis.Client, *miniredis.Miniredis) {
	t.Helper()

	mr, err := miniredis.Run()
	require.NoError(t, err)

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return client, mr
}

func seedRule(t *testing.T, db *gorm.DB, rule milestonedal.MilestoneRule) {
	t.Helper()
	require.NoError(t, db.Create(&rule).Error)
}

func seedItemContext(t *testing.T, db *gorm.DB, itemID, authorAgentID int64, summary string) {
	t.Helper()
	require.NoError(t, db.Create(&itemdal.ItemStats{
		ItemID:        itemID,
		AuthorAgentID: authorAgentID,
		CreatedAt:     1000,
		UpdatedAt:     1000,
	}).Error)
	require.NoError(t, db.Create(&itemdal.ProcessedItem{
		ItemID:    itemID,
		Status:    3,
		Summary:   summary,
		UpdatedAt: 1000,
	}).Error)
}

func TestRuleCacheCachesUntilTTLExpires(t *testing.T) {
	db := setupMilestoneTestDB(t)
	now := time.Unix(1, 0)

	seedRule(t, db, milestonedal.MilestoneRule{
		RuleID:          1,
		MetricKey:       MetricConsumed,
		Threshold:       50,
		RuleEnabled:     true,
		ContentTemplate: "first",
		CreatedAt:       1000,
		UpdatedAt:       1000,
	})

	cache := NewRuleCache(db, time.Minute)
	cache.now = func() time.Time { return now }

	ctx := context.Background()
	rules, err := cache.GetEnabledRules(ctx, MetricConsumed)
	require.NoError(t, err)
	require.Len(t, rules, 1)
	assert.Equal(t, "first", rules[0].ContentTemplate)

	require.NoError(t, db.Model(&milestonedal.MilestoneRule{}).
		Where("rule_id = ?", 1).
		Update("content_template", "second").Error)

	rules, err = cache.GetEnabledRules(ctx, MetricConsumed)
	require.NoError(t, err)
	require.Len(t, rules, 1)
	assert.Equal(t, "first", rules[0].ContentTemplate)

	now = now.Add(2 * time.Minute)
	rules, err = cache.GetEnabledRules(ctx, MetricConsumed)
	require.NoError(t, err)
	require.Len(t, rules, 1)
	assert.Equal(t, "second", rules[0].ContentTemplate)
}

func TestRuleCacheInvalidateForcesReloadBeforeTTL(t *testing.T) {
	db := setupMilestoneTestDB(t)
	now := time.Unix(1, 0)

	seedRule(t, db, milestonedal.MilestoneRule{
		RuleID:          11,
		MetricKey:       MetricConsumed,
		Threshold:       50,
		RuleEnabled:     true,
		ContentTemplate: "first",
		CreatedAt:       1000,
		UpdatedAt:       1000,
	})

	cache := NewRuleCache(db, time.Hour)
	cache.now = func() time.Time { return now }

	ctx := context.Background()
	rules, err := cache.GetEnabledRules(ctx, MetricConsumed)
	require.NoError(t, err)
	require.Len(t, rules, 1)
	assert.Equal(t, "first", rules[0].ContentTemplate)

	require.NoError(t, db.Model(&milestonedal.MilestoneRule{}).
		Where("rule_id = ?", 11).
		Update("content_template", "second").Error)

	cache.Invalidate(MetricConsumed)

	rules, err = cache.GetEnabledRules(ctx, MetricConsumed)
	require.NoError(t, err)
	require.Len(t, rules, 1)
	assert.Equal(t, "second", rules[0].ContentTemplate)
}

func TestNotificationStorePutListDelete(t *testing.T) {
	rdb, mr := setupMilestoneTestRedis(t)
	defer mr.Close()

	store := NewNotificationStore(rdb, DefaultNotificationTTL)
	ctx := context.Background()

	require.NoError(t, store.Put(ctx, 1001, Notification{
		NotificationID: "2",
		Type:           NotificationTypeMilestone,
		Content:        "second",
		CreatedAt:      20,
	}))
	require.NoError(t, store.Put(ctx, 1001, Notification{
		NotificationID: "1",
		Type:           NotificationTypeMilestone,
		Content:        "first",
		CreatedAt:      10,
	}))

	notifications, err := store.List(ctx, 1001)
	require.NoError(t, err)
	require.Len(t, notifications, 2)
	assert.Equal(t, "1", notifications[0].NotificationID)
	assert.Equal(t, "2", notifications[1].NotificationID)

	ttl := mr.TTL(NotificationKey(1001))
	assert.Greater(t, ttl, time.Duration(0))

	require.NoError(t, store.Delete(ctx, 1001, []int64{1}))
	notifications, err = store.List(ctx, 1001)
	require.NoError(t, err)
	require.Len(t, notifications, 1)
	assert.Equal(t, "2", notifications[0].NotificationID)
}

func TestServiceCheckCreatesEventsAndIsIdempotent(t *testing.T) {
	db := setupMilestoneTestDB(t)
	rdb, mr := setupMilestoneTestRedis(t)
	defer mr.Close()

	seedRule(t, db, milestonedal.MilestoneRule{
		RuleID:          101,
		MetricKey:       MetricConsumed,
		Threshold:       50,
		RuleEnabled:     true,
		ContentTemplate: `Your Content "{{.ItemSummary}}" reached {{.CounterValue}} consumptions. Item Id {{.ItemID}}`,
		CreatedAt:       1000,
		UpdatedAt:       1000,
	})
	seedRule(t, db, milestonedal.MilestoneRule{
		RuleID:          102,
		MetricKey:       MetricConsumed,
		Threshold:       500,
		RuleEnabled:     true,
		ContentTemplate: `Your Content "{{.ItemSummary}}" reached {{.CounterValue}} consumptions. Item Id {{.ItemID}}`,
		CreatedAt:       1000,
		UpdatedAt:       1000,
	})
	seedItemContext(t, db, 42, 2001, "Portable battery storage\nwith long summary")

	idGen := &fakeIDGenerator{next: []int64{7001, 7002, 7003, 7004}}
	svc, err := NewService(
		db,
		rdb,
		idGen,
		WithNowMillis(func() int64 { return 1700000000000 }),
		WithSummaryMaxRunes(20),
	)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, svc.Check(ctx, 42, MetricConsumed, 500))
	require.NoError(t, svc.Check(ctx, 42, MetricConsumed, 500))

	var events []milestonedal.MilestoneEvent
	require.NoError(t, db.Order("threshold ASC").Find(&events).Error)
	require.Len(t, events, 2)
	assert.Equal(t, int64(50), events[0].Threshold)
	assert.Equal(t, int64(500), events[1].Threshold)
	assert.Equal(t, milestonedal.NotificationStatusPending, events[0].NotificationStatus)
	assert.Equal(t, `Your Content "Portable battery sto" reached 500 consumptions. Item Id 42`, events[0].NotificationContent)
	assert.Equal(t, `Your Content "Portable battery sto" reached 500 consumptions. Item Id 42`, events[1].NotificationContent)

	notifications, err := svc.notifications.List(ctx, 2001)
	require.NoError(t, err)
	require.Len(t, notifications, 2)
	assert.Equal(t, "7001", notifications[0].NotificationID)
	assert.Equal(t, "7002", notifications[1].NotificationID)
}

func TestServiceCheckIgnoresDisabledRules(t *testing.T) {
	db := setupMilestoneTestDB(t)
	rdb, mr := setupMilestoneTestRedis(t)
	defer mr.Close()

	seedRule(t, db, milestonedal.MilestoneRule{
		RuleID:          201,
		MetricKey:       MetricScore1,
		Threshold:       50,
		RuleEnabled:     false,
		ContentTemplate: `noop`,
		CreatedAt:       1000,
		UpdatedAt:       1000,
	})
	seedItemContext(t, db, 88, 3001, "summary")

	svc, err := NewService(db, rdb, &fakeIDGenerator{next: []int64{9001}})
	require.NoError(t, err)

	require.NoError(t, svc.Check(context.Background(), 88, MetricScore1, 50))

	var count int64
	require.NoError(t, db.Model(&milestonedal.MilestoneEvent{}).Count(&count).Error)
	assert.Equal(t, int64(0), count)
}

func TestServiceRecoverPendingNotificationsAndMarkNotified(t *testing.T) {
	db := setupMilestoneTestDB(t)
	rdb, mr := setupMilestoneTestRedis(t)
	defer mr.Close()

	require.NoError(t, db.Create(&milestonedal.MilestoneEvent{
		EventID:             8101,
		ItemID:              101,
		AuthorAgentID:       4001,
		RuleID:              301,
		MetricKey:           MetricConsumed,
		Threshold:           50,
		CounterValue:        60,
		NotificationContent: `Your Content "summary" reached 60 consumptions. Item Id 101`,
		NotificationStatus:  milestonedal.NotificationStatusPending,
		QueuedAt:            1700000000100,
		TriggeredAt:         1700000000100,
	}).Error)
	require.NoError(t, db.Create(&milestonedal.MilestoneEvent{
		EventID:             8102,
		ItemID:              102,
		AuthorAgentID:       4001,
		RuleID:              302,
		MetricKey:           MetricConsumed,
		Threshold:           500,
		CounterValue:        600,
		NotificationContent: `Your Content "summary" reached 600 consumptions. Item Id 102`,
		NotificationStatus:  milestonedal.NotificationStatusNotified,
		QueuedAt:            1700000000200,
		TriggeredAt:         1700000000200,
		NotifiedAt:          1700000000300,
	}).Error)

	svc, err := NewService(
		db,
		rdb,
		&fakeIDGenerator{next: []int64{1}},
		WithNowMillis(func() int64 { return 1700000000400 }),
	)
	require.NoError(t, err)

	recovered, err := svc.RecoverPendingNotifications(context.Background(), 100)
	require.NoError(t, err)
	assert.Equal(t, 1, recovered)

	notifications, err := svc.notifications.List(context.Background(), 4001)
	require.NoError(t, err)
	require.Len(t, notifications, 1)
	assert.Equal(t, "8101", notifications[0].NotificationID)

	// Simulate ack: delete from Redis + mark notified in DB
	require.NoError(t, svc.notifications.Delete(context.Background(), 4001, []int64{8101}))
	require.NoError(t, milestonedal.MarkEventsNotified(context.Background(), db, []int64{8101}, 1700000000400))

	notifications, err = svc.notifications.List(context.Background(), 4001)
	require.NoError(t, err)
	assert.Empty(t, notifications)

	var event milestonedal.MilestoneEvent
	require.NoError(t, db.Where("event_id = ?", 8101).First(&event).Error)
	assert.Equal(t, milestonedal.NotificationStatusNotified, event.NotificationStatus)
	assert.Equal(t, int64(1700000000400), event.NotifiedAt)
}

func TestServiceRecoverPendingNotificationsWalksAllBatches(t *testing.T) {
	db := setupMilestoneTestDB(t)
	rdb, mr := setupMilestoneTestRedis(t)
	defer mr.Close()

	events := []milestonedal.MilestoneEvent{
		{
			EventID:             8201,
			ItemID:              201,
			AuthorAgentID:       5001,
			RuleID:              401,
			MetricKey:           MetricConsumed,
			Threshold:           2,
			CounterValue:        2,
			NotificationContent: `first`,
			NotificationStatus:  milestonedal.NotificationStatusPending,
			QueuedAt:            1700000000100,
			TriggeredAt:         1700000000100,
		},
		{
			EventID:             8202,
			ItemID:              202,
			AuthorAgentID:       5001,
			RuleID:              402,
			MetricKey:           MetricConsumed,
			Threshold:           2,
			CounterValue:        2,
			NotificationContent: `second`,
			NotificationStatus:  milestonedal.NotificationStatusPending,
			QueuedAt:            1700000000100,
			TriggeredAt:         1700000000100,
		},
		{
			EventID:             8203,
			ItemID:              203,
			AuthorAgentID:       5001,
			RuleID:              403,
			MetricKey:           MetricConsumed,
			Threshold:           2,
			CounterValue:        2,
			NotificationContent: `third`,
			NotificationStatus:  milestonedal.NotificationStatusPending,
			QueuedAt:            1700000000200,
			TriggeredAt:         1700000000200,
		},
	}
	require.NoError(t, db.Create(&events).Error)

	svc, err := NewService(db, rdb, &fakeIDGenerator{next: []int64{1}})
	require.NoError(t, err)

	recovered, err := svc.RecoverPendingNotifications(context.Background(), 2)
	require.NoError(t, err)
	assert.Equal(t, 3, recovered)

	notifications, err := svc.notifications.List(context.Background(), 5001)
	require.NoError(t, err)
	require.Len(t, notifications, 3)
	assert.Equal(t, "8201", notifications[0].NotificationID)
	assert.Equal(t, "8202", notifications[1].NotificationID)
	assert.Equal(t, "8203", notifications[2].NotificationID)
}

func TestServiceCheckRejectsInvalidMetric(t *testing.T) {
	db := setupMilestoneTestDB(t)
	rdb, mr := setupMilestoneTestRedis(t)
	defer mr.Close()

	svc, err := NewService(db, rdb, &fakeIDGenerator{next: []int64{1}})
	require.NoError(t, err)

	err = svc.Check(context.Background(), 1, "unknown", 1)
	assert.ErrorIs(t, err, ErrInvalidMetricKey)
}

func TestSubscribeRuleInvalidation(t *testing.T) {
	rdb, mr := setupMilestoneTestRedis(t)
	defer mr.Close()
	defer rdb.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	received := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		errCh <- SubscribeRuleInvalidation(ctx, rdb, func(metricKey string) {
			received <- metricKey
			cancel()
		})
	}()

	require.Eventually(t, func() bool {
		return len(mr.PubSubChannels("")) == 1
	}, time.Second, 10*time.Millisecond)

	require.NoError(t, PublishRuleInvalidation(context.Background(), rdb, MetricScore2))

	select {
	case metricKey := <-received:
		assert.Equal(t, MetricScore2, metricKey)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for milestone invalidation message")
	}

	require.NoError(t, <-errCh)
}
