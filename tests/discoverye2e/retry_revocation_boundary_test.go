package discoverye2e

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"eigenflux_server/pkg/dashboardsearch"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/pkg/need"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// These opt-in cases own application processes and fixture IDs, but never reset
// the shared infrastructure. Reject non-loopback dependencies before startup.
func authRetryFixture(t *testing.T) {
	t.Helper()
	if os.Getenv("DISCOVERY_E2E") != "1" {
		t.Skip("set DISCOVERY_E2E=1 with disposable loopback infrastructure")
	}
	require.Equal(t, "test", os.Getenv("APP_ENV"))
	loopback := func(host string) {
		t.Helper()
		ip := net.ParseIP(host)
		require.True(t, host == "localhost" || (ip != nil && ip.IsLoopback()), "fixture dependency must be loopback")
	}
	for _, name := range []string{"PG_DSN", "ES_URL"} {
		u, err := url.Parse(os.Getenv(name))
		require.NoError(t, err)
		require.NotEmpty(t, u.Hostname(), "explicit %s is required", name)
		loopback(u.Hostname())
	}
	for _, name := range []string{"REDIS_ADDR", "ETCD_ADDR"} {
		for _, address := range strings.Split(os.Getenv(name), ",") {
			host, _, err := net.SplitHostPort(address)
			require.NoError(t, err, "explicit %s host:port is required", name)
			loopback(host)
		}
	}
}

type authRetryHTTPResult struct {
	index  int
	status int
	data   json.RawMessage
	code   string
	err    error
}

func authRetryRequest(ctx context.Context, s *stack, method, path, token, key string, body any) authRetryHTTPResult {
	raw, err := json.Marshal(body)
	if err != nil {
		return authRetryHTTPResult{err: err}
	}
	req, err := http.NewRequestWithContext(ctx, method, s.url+path, bytes.NewReader(raw))
	if err != nil {
		return authRetryHTTPResult{err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := (&http.Client{Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}).Do(req)
	if err != nil {
		return authRetryHTTPResult{err: err}
	}
	defer resp.Body.Close()
	raw, err = io.ReadAll(resp.Body)
	if err != nil {
		return authRetryHTTPResult{status: resp.StatusCode, err: err}
	}
	var out struct {
		Data  json.RawMessage `json:"data"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	err = json.Unmarshal(raw, &out)
	return authRetryHTTPResult{status: resp.StatusCode, data: out.Data, code: out.Error.Code, err: err}
}

// A channel releases callers together; PostgreSQL lock observations below prove
// the write requests reached their actual transactions before the lock opens.
func authRetryConcurrent(t *testing.T, count int, send func(context.Context, int) authRetryHTTPResult) <-chan authRetryHTTPResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	t.Cleanup(cancel)
	start := make(chan struct{})
	results := make(chan authRetryHTTPResult, count)
	for i := range count {
		go func() {
			<-start
			result := send(ctx, i)
			result.index = i
			results <- result
		}()
	}
	close(start)
	return results
}

func authRetryReceive(t *testing.T, results <-chan authRetryHTTPResult) authRetryHTTPResult {
	t.Helper()
	select {
	case result := <-results:
		require.NoError(t, result.err)
		return result
	case <-time.After(13 * time.Second):
		t.Fatal("concurrent HTTP request did not finish")
		return authRetryHTTPResult{}
	}
}

// The separate connection holds either an advisory or a row lock. Cleanup
// always rolls it back, including failed barrier/assertion paths.
func authRetryDBBarrier(t *testing.T, s *stack, query string, args ...any) (int, func()) {
	t.Helper()
	db, err := s.db.DB()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := db.Conn(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })
	var pid int
	require.NoError(t, conn.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid))
	txCtx, cancelTx := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancelTx)
	tx, err := conn.BeginTx(txCtx, &sql.TxOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := tx.Rollback(); err != nil && err != sql.ErrTxDone {
			t.Errorf("barrier rollback: %v", err)
		}
	})
	_, err = tx.ExecContext(ctx, query, args...)
	require.NoError(t, err)
	var once sync.Once
	return pid, func() { once.Do(func() { require.NoError(t, tx.Commit()) }) }
}

func authRetryWaitBlocked(t *testing.T, s *stack, holderPID, count int) {
	t.Helper()
	// Include lock queues behind the first waiter, rather than assuming every
	// competing row writer is directly blocked on the holder's transaction ID.
	const query = `WITH RECURSIVE blocked(pid) AS (
		SELECT pid FROM pg_stat_activity WHERE ? = ANY(pg_blocking_pids(pid))
		UNION
		SELECT a.pid FROM pg_stat_activity a JOIN blocked b ON b.pid = ANY(pg_blocking_pids(a.pid))
	) SELECT count(*) FROM blocked b JOIN pg_stat_activity a USING(pid) WHERE a.wait_event_type='Lock'`
	var waiting int64
	var lastErr error
	require.Eventually(t, func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		lastErr = s.db.WithContext(ctx).Raw(query, holderPID).Scan(&waiting).Error
		return lastErr == nil && waiting >= int64(count)
	}, 4*time.Second, 10*time.Millisecond, "HTTP transactions must be observed waiting on the held database lock")
	require.NoError(t, lastErr)
	t.Logf("database barrier: holder=%d blocked_writers=%d", holderPID, waiting)
}

// Register before startStack so this check runs after its processes, fixture
// cleanup and borrowed Redis pool stop. A separate client remains alive solely
// to observe actual cleanup and to remove the unrelated survivor afterwards.
func authRetrySeedCleanupProbe(t *testing.T) func(*stack) {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: os.Getenv("REDIS_ADDR"), Password: os.Getenv("REDIS_PASSWORD"),
		DialTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second, ContextTimeoutEnabled: true})
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	var owned string
	survivor := fmt.Sprintf("auth-retry-cleanup-survivor:%d", time.Now().UnixNano())
	t.Cleanup(func() {
		if owned == "" {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		defer func() {
			if err := client.Del(ctx, owned, survivor).Err(); err != nil {
				t.Errorf("cleanup probe resource removal: %v", err)
			}
		}()
		remaining, err := client.Exists(ctx, owned).Result()
		require.NoError(t, err)
		require.Zero(t, remaining, "the real fixture cleanup must remove a terminal owner key")
		value, err := client.Get(ctx, survivor).Result()
		require.NoError(t, err)
		require.Equal(t, "unrelated-fixture-survivor", value, "cleanup cannot remove another key's terminal ID")
		t.Log("fixture cleanup: owned terminal key removed; unrelated terminal key survived")
	})
	return func(s *stack) {
		owned = fmt.Sprintf("auth-retry-cleanup-owned:%d", s.owner)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, client.Set(ctx, owned, "owned-fixture", time.Hour).Err())
		require.NoError(t, client.Set(ctx, survivor, "unrelated-fixture-survivor", time.Hour).Err())
	}
}

func TestAuthRetryConcurrentNeedCapture(t *testing.T) {
	authRetryFixture(t)
	seedCleanupProbe := authRetrySeedCleanupProbe(t)
	s := startStack(t)
	seedCleanupProbe(s)
	input := s.need(t, "agent")
	key := fmt.Sprintf("concurrent-capture-%d", s.owner)
	pid, release := authRetryDBBarrier(t, s, "SELECT pg_advisory_xact_lock($1)", s.owner)
	results := authRetryConcurrent(t, 3, func(ctx context.Context, _ int) authRetryHTTPResult {
		return authRetryRequest(ctx, s, "POST", "/api/v2/need-inputs", s.token, key, input)
	})
	authRetryWaitBlocked(t, s, pid, 3)
	release()
	created, replayed := 0, 0
	var saved need.Record
	for range 3 {
		result := authRetryReceive(t, results)
		require.Empty(t, result.code)
		out := decode[struct {
			NeedInput need.Record `json:"need_input"`
			Replayed  bool        `json:"replayed"`
		}](t, result.data)
		require.Positive(t, out.NeedInput.NeedInputID)
		require.True(t, out.NeedInput.Eligible)
		if out.Replayed {
			require.Equal(t, http.StatusOK, result.status)
			replayed++
		} else {
			require.Equal(t, http.StatusCreated, result.status)
			created++
		}
		if saved.NeedInputID != 0 {
			require.Equal(t, saved, out.NeedInput, "retries must return the exact committed record")
		}
		saved = out.NeedInput
	}
	require.Equal(t, 1, created)
	require.Equal(t, 2, replayed)
	var count int64
	require.NoError(t, s.db.Table("need_inputs").Where("agent_id=? AND idempotency_key=?", s.owner, key).Count(&count).Error)
	require.EqualValues(t, 1, count)
	input.Target.Goal = "a different request under the same key"
	conflict := authRetryRequest(context.Background(), s, "POST", "/api/v2/need-inputs", s.token, key, input)
	require.NoError(t, conflict.err)
	require.Equal(t, http.StatusConflict, conflict.status)
	require.Equal(t, "IDEMPOTENCY_CONFLICT", conflict.code)
	persisted, err := (need.Store{DB: s.db}).Get(context.Background(), s.owner, saved.NeedInputID)
	require.NoError(t, err)
	// PostgreSQL jsonb adds insignificant whitespace that HTTP JSON encoding
	// removes. Compact only that formatting before comparing every record field.
	for _, raw := range []*json.RawMessage{&persisted.Input, &persisted.IntentSnapshot} {
		var compact bytes.Buffer
		require.NoError(t, json.Compact(&compact, *raw))
		*raw = compact.Bytes()
	}
	require.Equal(t, saved, persisted, "mismatched retries must not mutate the committed input")
	t.Log("same-key capture: 1 created, 2 exact replays, 1 rejected mismatched payload, 1 SQL row")
}

func TestAuthRetryContextRevisionCompetition(t *testing.T) {
	authRetryFixture(t)
	s := startStack(t)
	input := s.need(t, "agent")
	s.sql(t, `INSERT INTO agent_context_heads(agent_id,current_revision,active_revision,updated_at) VALUES(?,1,1,?)`, s.owner, time.Now().UnixMilli())
	path := fmt.Sprintf("/api/v2/agent-context/intent-actions/%d", input.IntentID)
	body := func(index int) map[string]any {
		return map[string]any{"expected_context_revision": 1, "idempotency_key": fmt.Sprintf("competing-revision-%d-%d", s.owner, index),
			"watch_for": fmt.Sprintf("concurrent revision winner %d", index), "trigger_when": "design request",
			"action_instruction": "summarize", "action_policy": "analyze_only", "priority": 10}
	}
	pid, release := authRetryDBBarrier(t, s, "SELECT agent_id FROM agent_context_heads WHERE agent_id=$1 FOR UPDATE", s.owner)
	results := authRetryConcurrent(t, 2, func(ctx context.Context, index int) authRetryHTTPResult {
		return authRetryRequest(ctx, s, "PUT", path, s.token, "", body(index))
	})
	authRetryWaitBlocked(t, s, pid, 2)
	release()
	winner, loser := -1, -1
	for range 2 {
		result := authRetryReceive(t, results)
		switch result.status {
		case http.StatusOK:
			require.Equal(t, -1, winner, "only one write can consume revision 1")
			winner = result.index
			out := decode[struct {
				Revision int64 `json:"context_revision"`
				Replay   bool  `json:"idempotent_replay"`
			}](t, result.data)
			require.EqualValues(t, 2, out.Revision)
			require.False(t, out.Replay)
		case http.StatusConflict:
			require.Equal(t, "REVISION_CONFLICT", result.code)
			loser = result.index
		default:
			t.Fatalf("unexpected competing writer status=%d error=%s", result.status, result.code)
		}
	}
	require.NotEqual(t, -1, winner)
	require.NotEqual(t, -1, loser)
	var revision, version, snapshots, receipts int64
	var watch string
	require.NoError(t, s.db.Raw("SELECT active_revision FROM agent_context_heads WHERE agent_id=?", s.owner).Scan(&revision).Error)
	require.NoError(t, s.db.Raw("SELECT version FROM agent_intent_actions WHERE intent_id=?", input.IntentID).Scan(&version).Error)
	require.NoError(t, s.db.Raw("SELECT watch_for FROM agent_intent_actions WHERE intent_id=?", input.IntentID).Scan(&watch).Error)
	require.EqualValues(t, 2, revision)
	require.EqualValues(t, 2, version)
	require.Equal(t, body(winner)["watch_for"], watch)
	replayed := authRetryRequest(context.Background(), s, "PUT", path, s.token, "", body(winner))
	require.NoError(t, replayed.err)
	require.Equal(t, http.StatusOK, replayed.status)
	var replay map[string]any
	require.NoError(t, json.Unmarshal(replayed.data, &replay))
	require.Equal(t, float64(2), replay["context_revision"])
	require.Equal(t, true, replay["idempotent_replay"])
	stale := authRetryRequest(context.Background(), s, "PUT", path, s.token, "", body(loser))
	require.NoError(t, stale.err)
	require.Equal(t, http.StatusConflict, stale.status)
	require.Equal(t, "REVISION_CONFLICT", stale.code)
	require.NoError(t, s.db.Table("agent_context_revisions").Where("agent_id=? AND revision>1", s.owner).Count(&snapshots).Error)
	require.NoError(t, s.db.Table("agent_idempotency_requests").Where("agent_id=? AND operation='intent_update'", s.owner).Count(&receipts).Error)
	require.EqualValues(t, 1, snapshots)
	require.EqualValues(t, 1, receipts)
	require.NoError(t, s.db.Raw("SELECT active_revision FROM agent_context_heads WHERE agent_id=?", s.owner).Scan(&revision).Error)
	require.EqualValues(t, 2, revision, "replaying either competitor cannot advance context again")
	t.Log("same-revision writes: 1 committed, 1 REVISION_CONFLICT, winner replayed, loser still stale, 1 new snapshot/receipt")
}

func TestAuthRetryWarmSearchRevocation(t *testing.T) {
	authRetryFixture(t)
	s := startStackWithServices(t, []string{"pm"}, map[string]string{"ENABLE_COMMUNICATION_V2": "true"})
	s.token = s.seedSession(t, s.owner, "{feed:read,relations:read,relations:write,context:read,context:write}")
	s.otherToken = s.seedSession(t, s.other, "{feed:read,relations:read}")
	authorToken := s.seedSession(t, s.author, "{relations:read,relations:write}")
	t.Cleanup(func() {
		s.sql(t, "DELETE FROM friend_requests WHERE from_uid IN ? OR to_uid IN ?", []int64{s.owner, s.author}, []int64{s.owner, s.author})
	})
	applied := decode[struct {
		RequestID string `json:"request_id"`
	}](t, s.call(t, "POST", "/api/v2/relations/friend-requests", s.token, "", map[string]any{"to_uid": strconv.FormatInt(s.author, 10), "greeting": "isolated revocation boundary"}, 200))
	require.NotEmpty(t, applied.RequestID)
	s.call(t, "POST", "/api/v2/relations/friend-requests/handle", authorToken, "", map[string]any{"request_id": applied.RequestID, "action": 1}, 200)
	path := "/api/v2/dashboard/search?type=friend&limit=10&q=" + strconv.FormatInt(s.author, 10)
	assertPage := func(result authRetryHTTPResult, visible bool) {
		t.Helper()
		require.NoError(t, result.err)
		require.Equal(t, http.StatusOK, result.status)
		require.Empty(t, result.code)
		out := decode[struct {
			Groups []dashboardsearch.Group `json:"groups"`
		}](t, result.data)
		require.Len(t, out.Groups, 1)
		require.Equal(t, "friend", out.Groups[0].Type)
		require.Empty(t, out.Groups[0].Error, "unavailable/rate-limited groups cannot pass as hidden results")
		if visible {
			require.Len(t, out.Groups[0].Items, 1)
			require.Equal(t, strconv.FormatInt(s.author, 10), out.Groups[0].Items[0].ID)
		} else {
			require.Empty(t, out.Groups[0].Items)
			require.NotContains(t, string(result.data), `"id":"`+strconv.FormatInt(s.author, 10)+`"`, "revoked results must not leak result IDs")
			require.NotContains(t, string(result.data), "/agent/"+s.shortIDs[s.author], "revoked results must not leak result links")
		}
	}
	get := func(token string) authRetryHTTPResult {
		return authRetryRequest(context.Background(), s, "GET", path, token, "", nil)
	}
	// The unrelated owner cannot see the private friendship before revocation.
	assertPage(get(s.otherToken), false)
	assertPage(get(s.token), true)
	assertPage(get(s.token), true)
	ctx := context.Background()
	cacheKeys, err := mq.RDB.Keys(ctx, fmt.Sprintf("search:cache:v1:pm-friend:%d:*", s.owner)).Result()
	require.NoError(t, err)
	require.Len(t, cacheKeys, 1, "the test must hit a real warm PM search cache")
	cacheKey := cacheKeys[0]
	original, err := mq.RDB.Get(ctx, cacheKey).Bytes()
	require.NoError(t, err)
	require.Contains(t, string(original), strconv.FormatInt(s.author, 10))
	// The business DELETE now reaches PM and waits on the real relation row.
	// Reads while it waits are legitimately authorized by the committed state.
	pid, release := authRetryDBBarrier(t, s, "SELECT id FROM user_relations WHERE from_uid=$1 AND to_uid=$2 AND rel_type=1 FOR UPDATE", s.owner, s.author)
	unfriend := authRetryConcurrent(t, 1, func(ctx context.Context, _ int) authRetryHTTPResult {
		return authRetryRequest(ctx, s, "POST", "/api/v2/relations/friends/unfriend", s.token, "", map[string]any{"to_uid": strconv.FormatInt(s.author, 10)})
	})
	authRetryWaitBlocked(t, s, pid, 1)
	assertPage(get(s.token), true)
	release()
	removed := authRetryReceive(t, unfriend)
	require.Equal(t, http.StatusOK, removed.status)
	require.Empty(t, removed.code)
	var count int64
	require.NoError(t, s.db.Table("user_relations").Where("from_uid IN ? AND to_uid IN ? AND rel_type=1", []int64{s.owner, s.author}, []int64{s.owner, s.author}).Count(&count).Error)
	require.Zero(t, count, "the actual unfriend writer must delete both directions before post-commit reads")
	reads := authRetryConcurrent(t, 4, func(ctx context.Context, _ int) authRetryHTTPResult {
		return authRetryRequest(ctx, s, "GET", path, s.token, "", nil)
	})
	for range 4 {
		assertPage(authRetryReceive(t, reads), false)
	}
	assertPage(get(s.otherToken), false)
	stillCached, err := mq.RDB.Get(ctx, cacheKey).Bytes()
	require.NoError(t, err, "cache must still exist; expiry would not exercise warm-cache visibility")
	require.Equal(t, original, stillCached, "old private hit must remain in cache while each returned page revalidates current permission")
	ttl, err := mq.RDB.PTTL(ctx, cacheKey).Result()
	require.NoError(t, err)
	require.Positive(t, ttl)
	t.Logf("warm-cache revocation: real unfriend observed blocked; pre-commit visible; 4 concurrent post-commit reads hidden; unchanged cache TTL=%s", ttl)
}
