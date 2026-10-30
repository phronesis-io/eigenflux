package dal

import (
	"fmt"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSlopPostgresConcurrentCheckpoint(t *testing.T) {
	dsn := os.Getenv("SLOP_PG_TEST_DSN")
	if dsn == "" {
		t.Skip("SLOP_PG_TEST_DSN required")
	}
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	require.Contains(t, []string{"127.0.0.1", "localhost"}, u.Hostname(), "isolated loopback database required")
	require.True(t, strings.Contains(u.Path, "slop_validation"), "dedicated test database required")
	base, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	schema := fmt.Sprintf("slop_race_%d", time.Now().UnixNano())
	require.NoError(t, base.Exec("CREATE SCHEMA "+schema).Error)
	defer base.Exec("DROP SCHEMA " + schema + " CASCADE")
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	gdb, err := gorm.Open(postgres.Open(u.String()), &gorm.Config{})
	require.NoError(t, err)
	pool, err := gdb.DB()
	require.NoError(t, err)
	defer pool.Close()
	require.NoError(t, gdb.AutoMigrate(&ProcessedItem{}, &ItemSlopEvaluation{}))
	require.NoError(t, gdb.Create(&ProcessedItem{ItemID: 1, Status: StatusProcessing}).Error)
	start := make(chan struct{})
	var wg sync.WaitGroup
	saved := make([]*ItemSlopEvaluation, 2)
	errs := make([]error, 2)
	for i := range saved {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			label := i
			quality := 0.8
			effective := quality
			if i == 1 {
				effective = 0
			}
			score := float64(i)
			saved[i], errs[i] = SaveSlopEvaluation(gdb, &ItemSlopEvaluation{ItemID: 1, PromptVersion: "V0.2.1", ContentHash: "same", IsSlop: &label, SlopScore: &score, SlopReason: "S2", ModelQuality: &quality, EffectiveQuality: &effective, Blocked: i == 1})
		}(i)
	}
	close(start)
	wg.Wait()
	for _, err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, saved[0].Blocked, saved[1].Blocked)
	var n int64
	require.NoError(t, gdb.Model(&ItemSlopEvaluation{}).Count(&n).Error)
	require.Equal(t, int64(1), n)
	p, err := GetProcessedItemByID(gdb, 1)
	require.NoError(t, err)
	require.Equal(t, saved[0].Blocked, p.Status == StatusDiscarded)
	// A stale evaluation must not introduce a new block on completed history.
	require.NoError(t, gdb.Create(&ProcessedItem{ItemID: 3, Status: StatusCompleted}).Error)
	_, err = SaveSlopEvaluation(gdb, &ItemSlopEvaluation{ItemID: 3, PromptVersion: "V0.2.1", ContentHash: "complete", Blocked: true})
	require.Error(t, err)
	completedAudit, err := GetSlopEvaluation(gdb, 3)
	require.NoError(t, err)
	require.Nil(t, completedAudit)
	// A failed blocked-state update must also roll back the audit insert.
	require.NoError(t, gdb.Create(&ProcessedItem{ItemID: 2, Status: StatusProcessing}).Error)
	require.NoError(t, gdb.Exec(`CREATE FUNCTION reject_block() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.status=4 THEN RAISE EXCEPTION 'simulated failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_block BEFORE UPDATE ON processed_items FOR EACH ROW EXECUTE FUNCTION reject_block()`).Error)
	one := 1
	zero := 0.0
	_, err = SaveSlopEvaluation(gdb, &ItemSlopEvaluation{ItemID: 2, PromptVersion: "V0.2.1", ContentHash: "new", IsSlop: &one, Blocked: true, EffectiveQuality: &zero})
	require.Error(t, err)
	row, err := GetSlopEvaluation(gdb, 2)
	require.NoError(t, err)
	require.Nil(t, row)
}
