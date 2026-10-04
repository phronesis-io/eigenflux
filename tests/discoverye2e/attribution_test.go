package discoverye2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"eigenflux_server/pipeline/consumer"
	"eigenflux_server/pkg/idgen"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/pkg/recall"
	"github.com/stretchr/testify/require"
)

// Use the production consumers so a successful HTTP write alone cannot pass
// this check: the same exposure must survive all the way to PostgreSQL.
func (s *stack) checkCLIAttribution(t *testing.T, binary, home, impression string) {
	t.Helper()
	s.sql(t, "INSERT INTO item_stats(item_id,author_agent_id,created_at,updated_at) VALUES(?,?,?,?)", s.item, s.author, time.Now().UnixMilli(), time.Now().UnixMilli())
	ids, err := idgen.NewManagedGenerator(context.Background(), idgen.ManagedGeneratorConfig{Endpoints: strings.Split(s.cfg.EtcdAddr, ","), WorkerPrefix: s.cfg.IDWorkerPrefix, ServiceName: "attribution-e2e", LeaseTTLSecond: s.cfg.IDWorkerLeaseTTL, EpochMS: s.cfg.IDSnowflakeEpoch})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	var workers sync.WaitGroup
	workers.Add(2)
	go func() { defer workers.Done(); consumer.NewItemStatsConsumer(s.cfg, nil).Start(ctx) }()
	go func() {
		defer workers.Done()
		consumer.NewFollowupConsumer(ids, recall.NewSurfaceHistoryStore(mq.RDB, "rec")).Start(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		workers.Wait()
		require.NoError(t, ids.Close(context.Background()))
		require.NoError(t, s.db.Exec("DELETE FROM item_stats WHERE item_id = ?", s.item).Error)
		require.NoError(t, s.db.Exec("DELETE FROM feedback_logs WHERE agent_id = ?", s.owner).Error)
		require.NoError(t, s.db.Exec("DELETE FROM followup_labels WHERE agent_id = ?", s.owner).Error)
	})
	run := func(args ...string) {
		t.Helper()
		command := exec.CommandContext(ctx, binary, append([]string{"--homedir", home, "--format", "json"}, args...)...)
		command.Env = append(os.Environ(), "EIGENFLUX_SKILLS_BASE_URL=http://127.0.0.1:1")
		out, err := command.CombinedOutput()
		require.NoError(t, err, "CLI: %s", out)
	}
	run("feed", "feedback", "--items", fmt.Sprintf(`[{"item_id":"%d","score":0}]`, s.item))
	run("feed", "event", "record", "--item-ids", fmt.Sprint(s.item), "--impression-id", impression, "--kind", "surface")
	require.Eventually(t, func() bool {
		var count int64
		err := s.db.Raw(`SELECT count(*) FROM replay_logs r JOIN feedback_logs f ON f.impression_id=r.impression_id AND f.agent_id=r.agent_id AND f.item_id=r.item_id JOIN followup_labels l ON l.impression_id=r.impression_id AND l.agent_id=r.agent_id AND l.item_id=r.item_id WHERE r.impression_id=? AND r.agent_id=? AND r.item_id=? AND r.source_kind='broadcast' AND l.kind='surface'`, impression, s.owner, s.item).Scan(&count).Error
		return err == nil && count == 1
	}, 5*time.Second, 25*time.Millisecond, "feedback and behavior must join the exact delivered broadcast")
	require.Eventually(t, func() bool {
		var count int64
		err := s.db.Raw(`SELECT count(*) FROM item_stats s WHERE s.item_id=? AND s.consumed_count=(SELECT count(*) FROM replay_logs r WHERE r.item_id=s.item_id AND r.delivered IS TRUE) AND s.consumed_count>0`, s.item).Scan(&count).Error
		return err == nil && count == 1
	}, 5*time.Second, 25*time.Millisecond, "consumption must match actual deliveries, excluding retries and other kinds")
}
