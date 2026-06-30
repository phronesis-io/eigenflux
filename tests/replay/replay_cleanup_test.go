package replay_test

import (
	"strconv"
	"testing"
	"time"

	"eigenflux_server/pipeline/consumer"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/tests/testutil"

	"gorm.io/gorm/logger"
)

func TestMain(m *testing.M) {
	testutil.RunTestMain(m)
}

const replayTestImprPrefix = "test-replay-cleanup-"

// TestDeleteOldReplayLogs verifies the cron's purge DAL drops rows older than
// the cutoff (across multiple batches) while leaving recent rows untouched.
func TestDeleteOldReplayLogs(t *testing.T) {
	cfg := config.Load()
	db.InitWithLogLevel(cfg.PgDSN, logger.Silent)

	clean := func() {
		db.DB.Exec("DELETE FROM replay_logs WHERE impression_id LIKE ?", replayTestImprPrefix+"%")
	}
	clean()
	t.Cleanup(clean)

	const dayMs = int64(24 * 60 * 60 * 1000)
	now := time.Now().UnixMilli()
	const agentID int64 = 9_100_000_000_000_000_001
	idBase := int64(9_100_000_000_000_000_100)

	// impression_id is unique per row (unique index on impression_id, position).
	insert := func(seq int, servedAt int64) {
		err := db.DB.Exec(
			`INSERT INTO replay_logs (id, impression_id, agent_id, item_id, served_at, created_at, position)
			 VALUES (?, ?, ?, ?, ?, ?, 0)`,
			idBase+int64(seq), replayTestImprPrefix+strconv.Itoa(seq),
			agentID, idBase+int64(seq), servedAt, servedAt,
		).Error
		if err != nil {
			t.Fatalf("insert replay log seq=%d: %v", seq, err)
		}
	}

	oldServed := now - 40*dayMs
	recentServed := now - 1*dayMs
	const oldCount = 4
	const recentCount = 2
	for i := range oldCount {
		insert(i, oldServed)
	}
	for i := range recentCount {
		insert(oldCount+i, recentServed)
	}

	// Retain 30 days; batch size 2 forces the delete loop to iterate.
	cutoff := now - 30*dayMs
	deleted, err := consumer.DeleteOldReplayLogs(db.DB, cutoff, 2)
	if err != nil {
		t.Fatalf("DeleteOldReplayLogs: %v", err)
	}
	if deleted != oldCount {
		t.Fatalf("deleted = %d, want %d", deleted, oldCount)
	}

	var remaining int64
	if err := db.DB.Raw(
		"SELECT count(*) FROM replay_logs WHERE impression_id LIKE ?", replayTestImprPrefix+"%",
	).Scan(&remaining).Error; err != nil {
		t.Fatalf("count remaining: %v", err)
	}
	if remaining != recentCount {
		t.Fatalf("remaining = %d, want %d", remaining, recentCount)
	}
}
