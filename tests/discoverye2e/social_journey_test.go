package discoverye2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"eigenflux_server/pipeline/consumer"
	"eigenflux_server/pkg/bloomfilter"
	"eigenflux_server/pkg/es"
	"eigenflux_server/pkg/followuplog"
	"eigenflux_server/pkg/idgen"
	"eigenflux_server/pkg/impr"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/pkg/recall"
	sortdal "eigenflux_server/rpc/sort/dal"
	"eigenflux_server/rpc/sort/discovery"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestDiscoveryAPIFriendAndCLISurfaceJourney(t *testing.T) {
	binary := os.Getenv("EIGENFLUX_TEST_CLI")
	if binary == "" {
		t.Skip("EIGENFLUX_TEST_CLI required")
	}
	namespace := fmt.Sprintf("rec:social-journey-%d", time.Now().UnixNano())
	s := startStackWithServices(t, []string{"pm"}, map[string]string{
		"FRIEND_FEED_ENABLED": "true", "ENABLE_SWING_I2I_RECALL": "true", "REC_REDIS_NAMESPACE": namespace,
		"FRIEND_REQUEST_LIMITS_CONFIG": filepath.Join(t.TempDir(), "missing-test-limits.yaml"),
	})
	ctx := context.Background()
	s.token = s.seedSession(t, s.owner, "{feed:read,relations:read,relations:write,context:read,context:write}")
	s.otherToken = s.seedSession(t, s.other, "{feed:read,feed:feedback,context:read,context:write}")
	authorToken := s.seedSession(t, s.author, "{feed:read,relations:read,relations:write}")
	t.Cleanup(func() {
		s.sql(t, "DELETE FROM friend_requests WHERE from_uid IN ? OR to_uid IN ?", []int64{s.owner, s.author}, []int64{s.owner, s.author})
		s.sql(t, "DELETE FROM followup_labels WHERE agent_id=?", s.other)
		keys, err := mq.RDB.Keys(ctx, namespace+":*").Result()
		require.NoError(t, err)
		if len(keys) > 0 {
			require.NoError(t, mq.RDB.Del(ctx, keys...).Err())
		}
	})

	in := s.need(t, "broadcast")
	in.Target.Goal = "unrelated-quasar-physics"
	captured := s.capture(t, in)
	request := discovery.Request{SourceKinds: []discovery.Kind{discovery.Broadcast}, NeedIDs: []string{fmt.Sprint(captured.NeedInputID)}, Limit: 2}
	require.Empty(t, s.recommend(t, request, "").Items)
	accept := func() {
		t.Helper()
		applied := decode[struct {
			RequestID string `json:"request_id"`
		}](t, s.call(t, "POST", "/api/v2/relations/friend-requests", s.token, "", map[string]any{"to_uid": fmt.Sprint(s.author), "greeting": "isolated discovery journey"}, 200))
		require.NotEmpty(t, applied.RequestID)
		s.call(t, "POST", "/api/v2/relations/friend-requests/handle", authorToken, "", map[string]any{"request_id": applied.RequestID, "action": 1}, 200)
		var count int64
		require.NoError(t, s.db.Raw("SELECT count(*) FROM user_relations WHERE from_uid IN ? AND to_uid IN ? AND rel_type=1", []int64{s.owner, s.author}, []int64{s.owner, s.author}).Scan(&count).Error)
		require.EqualValues(t, 2, count, "the actual accept writer must create both friend directions")
	}
	accept()
	filtered := request
	filtered.Filters.Lang = []string{"zh"}
	require.Empty(t, s.recommend(t, filtered, "").Items, "friend recall cannot bypass language filters")
	capped := request
	capped.Limit = 1
	require.Empty(t, s.recommend(t, capped, "").Items, "one result slot has no friend capacity")
	friend := s.recommend(t, request, "api-friend-delivery")
	require.Len(t, friend.Items, 1)
	require.Equal(t, s.item, friend.Items[0].Ref.ID)
	s.assertRecallChannel(t, s.owner, friend.ImpressionID, "friend")
	s.call(t, "POST", "/api/v2/relations/friends/unfriend", s.token, "", map[string]any{"to_uid": fmt.Sprint(s.author)}, 200)
	s.clearBroadcastSeen(t, s.owner, s.item)
	require.Empty(t, s.recommend(t, request, "after-unfriend").Items, "unfriend must remove the recall route, independently of seen suppression")
	accept()
	s.call(t, "POST", "/api/v2/relations/friends/block", s.token, "", map[string]any{"to_uid": fmt.Sprint(s.author)}, 200)
	require.Empty(t, s.recommend(t, request, "after-block").Items, "block must exclude fresh friend recommendations")

	// The second broadcast and offline neighbor map are deterministic source
	// fixtures. The Surface seed itself must come only from CLI -> API -> the
	// production FollowupConsumer, never a direct SurfaceHistoryStore write.
	neighbor := s.item + 1000
	s.addSocialNeighbor(t, neighbor)
	require.NoError(t, mq.RDB.Set(ctx, namespace+":swing_i2i:active_version", "v1", time.Hour).Err())
	require.NoError(t, mq.RDB.Set(ctx, fmt.Sprintf("%s:swing_i2i:v1:item:%d:scored_neighbors", namespace, s.item), fmt.Sprintf("%d:1", neighbor), time.Hour).Err())
	s.changeInputs(t, `UPDATE agent_context_revisions SET compiled_context='{"intent_actions":[]}'::jsonb WHERE agent_id=?`, s.other)
	home := s.socialCLIHome(t, s.other, s.otherToken)
	run := func(args ...string) json.RawMessage {
		t.Helper()
		commandCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		command := exec.CommandContext(commandCtx, binary, append([]string{"--homedir", home, "--format", "json"}, args...)...)
		command.Env = append(os.Environ(), "EIGENFLUX_SKILLS_BASE_URL=http://127.0.0.1:1")
		var stderr bytes.Buffer
		command.Stderr = &stderr
		raw, err := command.Output()
		require.NoError(t, err, "CLI: %s", stderr.String())
		return raw
	}
	search := decode[discovery.Response](t, run("search", "landing page design", "--types", "broadcast"))
	require.Len(t, search.Items, 1)
	require.Equal(t, s.item, search.Items[0].Ref.ID)
	recommend := func(key string) discovery.Response {
		return decode[discovery.Response](t, run("recommend", "--types", "broadcast", "--limit", "2", "--idempotency-key", key))
	}
	require.Empty(t, recommend("before-surface").Items, "an actual search impression alone cannot seed Swing")
	ids, err := idgen.NewManagedGenerator(ctx, idgen.ManagedGeneratorConfig{Endpoints: strings.Split(s.cfg.EtcdAddr, ","), WorkerPrefix: s.cfg.IDWorkerPrefix, ServiceName: "social-journey-followup", LeaseTTLSecond: s.cfg.IDWorkerLeaseTTL, EpochMS: s.cfg.IDSnowflakeEpoch})
	require.NoError(t, err)
	workerCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		consumer.NewFollowupConsumer(ids, recall.NewSurfaceHistoryStore(mq.RDB, namespace)).Start(workerCtx)
	}()
	t.Cleanup(func() {
		stop()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("followup consumer did not stop within five seconds")
		}
		require.NoError(t, ids.Close(ctx))
	})
	record := func() {
		t.Helper()
		response := decode[struct {
			Accepted int `json:"accepted"`
		}](t, run("feed", "event", "record", "--item-ids", fmt.Sprint(s.item), "--kind", "surface"))
		require.Equal(t, 1, response.Accepted, "the CLI ledger must recognize and enrich its discovery item")
	}
	record()
	flushQueue := func() {
		t.Helper()
		flush := decode[struct {
			OK        bool `json:"ok"`
			Remaining int  `json:"remaining"`
		}](t, run("feed", "event", "flush"))
		require.True(t, flush.OK)
		require.Zero(t, flush.Remaining, "opportunistic flush errors cannot leave a retry queued")
	}
	flushQueue()
	require.Eventually(t, func() bool {
		var count int64
		err := s.db.Raw(`SELECT count(*) FROM replay_logs r JOIN followup_labels l ON l.impression_id=r.impression_id AND l.agent_id=r.agent_id AND l.item_id=r.item_id WHERE r.impression_id=? AND r.agent_id=? AND r.item_id=? AND l.kind='surface'`, search.ImpressionID, s.other, s.item).Scan(&count).Error
		return err == nil && count == 1
	}, 10*time.Second, 25*time.Millisecond, "the CLI event must join the exact search exposure")
	require.Eventually(t, func() bool {
		seeds, err := recall.NewSurfaceHistoryStore(mq.RDB, namespace).Recent(ctx, s.other, 10)
		return err == nil && len(seeds) == 1 && seeds[0] == s.item
	}, 5*time.Second, 25*time.Millisecond)
	trackedMessages := func() []redis.XMessage {
		t.Helper()
		messages, err := mq.RDB.XRange(ctx, followuplog.StreamName, "-", "+").Result()
		require.NoError(t, err)
		tracked := []redis.XMessage{}
		for _, message := range messages {
			if message.Values["agent_id"] == fmt.Sprint(s.other) && message.Values["item_id"] == fmt.Sprint(s.item) && message.Values["kind"] == "surface" && message.Values["impression_id"] == search.ImpressionID {
				tracked = append(tracked, message)
			}
		}
		return tracked
	}
	initial := trackedMessages()
	require.Len(t, initial, 1)
	record()
	flushQueue()
	replayed := trackedMessages()
	require.Len(t, replayed, 2, "the duplicate request must actually reach the API and append another stream message")
	require.NotEqual(t, initial[0].ID, replayed[1].ID)
	require.Equal(t, initial[0].Values["dedup_key"], replayed[1].Values["dedup_key"], "server-side deduplication must see the same event identity")
	// A missing PEL entry alone also describes a message never read. Require
	// the group's delivery cursor to pass both IDs, then observe their ACKs.
	require.Eventually(t, func() bool {
		groups, err := mq.RDB.XInfoGroups(ctx, followuplog.StreamName).Result()
		if err != nil {
			return false
		}
		for _, group := range groups {
			if group.Name != followuplog.GroupName || !streamIDAtLeast(group.LastDeliveredID, replayed[1].ID) {
				continue
			}
			for _, message := range replayed {
				pending, err := mq.RDB.XPendingExt(ctx, &redis.XPendingExtArgs{Stream: followuplog.StreamName, Group: followuplog.GroupName, Start: message.ID, End: message.ID, Count: 1}).Result()
				if err != nil || len(pending) != 0 {
					return false
				}
			}
			return true
		}
		return false
	}, 10*time.Second, 25*time.Millisecond, "both initial and replayed stream messages must be read and acknowledged")
	var labels int64
	require.NoError(t, s.db.Table("followup_labels").Where("agent_id=? AND item_id=? AND impression_id=? AND kind='surface'", s.other, s.item, search.ImpressionID).Count(&labels).Error)
	require.EqualValues(t, 1, labels, "an event retry must not duplicate the confirmed seed")
	seeds, err := recall.NewSurfaceHistoryStore(mq.RDB, namespace).Recent(ctx, s.other, 10)
	require.NoError(t, err)
	require.Equal(t, []int64{s.item}, seeds, "acknowledged retries must leave exactly one confirmed seed")
	swing := recommend("after-surface")
	require.Len(t, swing.Items, 1)
	require.Equal(t, neighbor, swing.Items[0].Ref.ID)
	s.assertRecallChannel(t, s.other, swing.ImpressionID, "swing_i2i")
	require.Equal(t, swing, recommend("after-surface"), "retry must retain the frozen exposure")
	require.Eventually(t, func() bool {
		return mq.RDB.SIsMember(ctx, bloomfilter.GetKeyForDate(time.Now()), fmt.Sprintf("%d:%d", s.other, neighbor)).Val()
	}, 3*time.Second, 25*time.Millisecond)
	require.Empty(t, recommend("after-delivery").Items, "a delivered neighbor must be suppressed on a fresh request")
}

func streamIDAtLeast(actual, required string) bool {
	a, b := strings.Split(actual, "-"), strings.Split(required, "-")
	if len(a) != 2 || len(b) != 2 {
		return false
	}
	for i := range a {
		left, leftErr := strconv.ParseUint(a[i], 10, 64)
		right, rightErr := strconv.ParseUint(b[i], 10, 64)
		if leftErr != nil || rightErr != nil {
			return false
		}
		if left != right {
			return left > right
		}
	}
	return true
}

func (s *stack) socialCLIHome(t *testing.T, owner int64, token string) string {
	t.Helper()
	home := filepath.Join(t.TempDir(), ".eigenflux")
	dir := filepath.Join(home, "servers", "eigenflux")
	require.NoError(t, os.MkdirAll(dir, 0700))
	writeJSON(t, filepath.Join(home, "config.json"), map[string]any{"default_server": "eigenflux", "servers": []any{map[string]any{"name": "eigenflux", "endpoint": s.url}}, "kv": map[string]string{"auto_skill_sync": "false"}})
	writeJSON(t, filepath.Join(dir, "agent-v2-credentials.json"), map[string]any{"access_token": token, "refresh_token": "test-only-refresh", "agent_id": fmt.Sprint(owner), "expires_at": time.Now().Add(time.Hour).UnixMilli()})
	return home
}

func (s *stack) assertRecallChannel(t *testing.T, owner int64, impression, channel string) {
	t.Helper()
	require.Eventually(t, func() bool {
		var count int64
		err := s.db.Table("replay_logs").Where("impression_id=? AND agent_id=?", impression, owner).Count(&count).Error
		return err == nil && count == 1
	}, 10*time.Second, 25*time.Millisecond)
	var raw string
	require.NoError(t, s.db.Table("replay_logs").Select("item_features").Where("impression_id=? AND agent_id=?", impression, owner).Scan(&raw).Error)
	candidate := decode[struct {
		Search discovery.Candidate `json:"search"`
	}](t, []byte(raw)).Search
	require.Contains(t, candidate.Document.Channels, channel)
}

func (s *stack) clearBroadcastSeen(t *testing.T, owner, group int64) {
	t.Helper()
	ctx := context.Background()
	key, member := bloomfilter.GetKeyForDate(time.Now()), fmt.Sprintf("%d:%d", owner, group)
	require.Eventually(t, func() bool { return mq.RDB.SIsMember(ctx, key, member).Val() }, 3*time.Second, 25*time.Millisecond)
	require.NoError(t, mq.RDB.SRem(ctx, key, member).Err())
	require.False(t, mq.RDB.SIsMember(ctx, key, member).Val())
	// Remove only this fixture's item and group; no shared history is reset.
	require.NoError(t, mq.RDB.SRem(ctx, fmt.Sprintf(impr.KeyItemIDs, owner), fmt.Sprint(group)).Err())
	require.NoError(t, mq.RDB.SRem(ctx, fmt.Sprintf(impr.KeyGroupIDs, owner), fmt.Sprint(group)).Err())
}

func (s *stack) addSocialNeighbor(t *testing.T, id int64) {
	t.Helper()
	now := time.Now().UnixMilli()
	s.sql(t, "INSERT INTO raw_items(item_id,author_agent_id,raw_content,created_at) VALUES(?,?,?,?)", id, s.author, "unrelated Swing neighbor", now)
	s.sql(t, "INSERT INTO processed_items(item_id,status,summary,broadcast_type,source_type,quality_score,lang,group_id,updated_at) VALUES(?,3,'unrelated Swing neighbor','info','original',0.8,'en',?,?)", id, id, now)
	vector := make([]float32, len(s.vector))
	vector[0] = -1 // This neighbor is recalled only through Swing, not the seed query.
	require.NoError(t, sortdal.IndexItem(context.Background(), &sortdal.Item{ID: id, AuthorAgentID: s.author, Content: "unrelated Swing neighbor", Summary: "unrelated Swing neighbor", Type: "info", SourceType: "original", Lang: "en", QualityScore: .8, GroupID: id, CreatedAt: time.UnixMilli(now), UpdatedAt: time.UnixMilli(now), Embedding: vector}))
	t.Cleanup(func() {
		s.sql(t, "DELETE FROM processed_items WHERE item_id=?", id)
		s.sql(t, "DELETE FROM raw_items WHERE item_id=?", id)
		response, err := es.Client.DeleteByQuery([]string{es.ReadIndexPattern}, bytes.NewBufferString(fmt.Sprintf(`{"query":{"term":{"id":%d}}}`, id)), es.Client.DeleteByQuery.WithRefresh(true))
		require.NoError(t, err)
		response.Body.Close()
		require.False(t, response.IsError())
		keys, err := mq.RDB.Keys(context.Background(), fmt.Sprintf("*:%d:*", id)).Result()
		require.NoError(t, err)
		if len(keys) > 0 {
			require.NoError(t, mq.RDB.Del(context.Background(), keys...).Err())
		}
	})
}
