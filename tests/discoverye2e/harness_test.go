package discoverye2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"eigenflux_server/pipeline/consumer"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/es"
	"eigenflux_server/pkg/idgen"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/rpc/sort/discovery"
	searchindex "eigenflux_server/rpc/sort/discovery/index"

	"github.com/stretchr/testify/require"
	clientv3 "go.etcd.io/etcd/client/v3"
	"gorm.io/gorm"
)

// These tests own the application processes. Infrastructure must be an isolated,
// migrated test stack with no other Kitex services registered in its etcd.
type stack struct {
	root, logs, url, category, commissionIndex, agentIndex string
	db                                                     *gorm.DB
	cfg                                                    *config.Config
	owner, other, author, item                             int64
	token, otherToken                                      string
	shortIDs                                               map[int64]string
	vector                                                 []float32
	catalogue                                              *catalogueFixture
}

func startStack(t *testing.T) *stack {
	t.Helper()
	if os.Getenv("DISCOVERY_E2E") != "1" {
		t.Skip("set DISCOVERY_E2E=1 with isolated migrated PG/Redis/ES/etcd and built services")
	}
	cfg := config.Load()
	require.Equal(t, "test", cfg.AppEnv, "this suite requires APP_ENV=test")
	require.NotEmpty(t, os.Getenv("PG_DSN"), "explicit isolated PG_DSN is required")
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	for _, name := range []string{"sort", "feed", "api", "item"} {
		_, err := os.Stat(filepath.Join(root, "build", name))
		require.NoError(t, err, "run bash scripts/common/build.sh first")
	}
	etcdClient, err := clientv3.New(clientv3.Config{Endpoints: strings.Split(cfg.EtcdAddr, ","), DialTimeout: 5 * time.Second})
	require.NoError(t, err)
	t.Cleanup(func() { _ = etcdClient.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	registrations, err := etcdClient.Get(ctx, "kitex/registry-etcd/", clientv3.WithPrefix(), clientv3.WithKeysOnly())
	cancel()
	require.NoError(t, err)
	require.Empty(t, registrations.Kvs, "use a dedicated etcd without other running RPC services")

	db.Init(cfg.PgDSN)
	sqlDB, err := db.DB.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	// Serialize with the existing integration suites without resetting their data.
	lock, err := sqlDB.Conn(context.Background())
	require.NoError(t, err)
	_, err = lock.ExecContext(context.Background(), "SELECT pg_advisory_lock(2026030501)")
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = lock.ExecContext(context.Background(), "SELECT pg_advisory_unlock(2026030501)")
		_ = lock.Close()
	})
	mq.Init(cfg.RedisAddr, cfg.RedisPassword)
	t.Cleanup(func() { _ = mq.RDB.Close() })
	require.NoError(t, es.InitES(cfg.EmbeddingDimensions))
	seed := time.Now().UnixNano() / 10
	s := &stack{root: root, db: db.DB, cfg: cfg, owner: seed, other: seed + 1, author: seed + 2, item: seed + 3,
		category: fmt.Sprintf("e2e-design-%d", seed), commissionIndex: fmt.Sprintf("discovery-e2e-c-%d", seed), agentIndex: fmt.Sprintf("discovery-e2e-a-%d", seed)}
	s.logs = filepath.Join(root, "build", fmt.Sprintf("discovery-e2e-%d", seed))
	require.NoError(t, os.MkdirAll(s.logs, 0700))
	t.Logf("process logs: %s", s.logs)
	s.vector = make([]float32, cfg.EmbeddingDimensions)
	s.vector[0] = 1
	embedding := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/embeddings" {
			http.NotFound(w, r)
			return
		}
		var input struct {
			Input string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			http.Error(w, "invalid embedding input", http.StatusBadRequest)
			return
		}
		// Exercise dictionary-only search while the existing optional embedding
		// dependency is unavailable; no generative model participates in this path.
		if input.Input == "著陸頁" {
			http.Error(w, "fixture embedding unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"index": 0, "embedding": s.vector}}, "model": "discovery-e2e", "usage": map[string]int{"total_tokens": 1}})
	}))
	t.Cleanup(embedding.Close)
	vocab := searchindex.Vocabulary{Version: s.category, EmbeddingVersion: "discovery-e2e", Categories: []searchindex.Node{{ID: s.category, Name: "Design"}}, Intents: []searchindex.Node{{ID: "landing-page", Name: "Landing page design", Category: s.category, Aliases: []string{"landing page", "着陆页", "著陸頁", "LP"}, Vector: s.vector}}}
	taxPath := filepath.Join(s.logs, "taxonomy.json")
	writeJSON(t, taxPath, vocab)
	_, err = searchindex.Configure(taxPath)
	require.NoError(t, err)
	rules := discovery.Rules{}
	for _, kind := range discovery.AllKinds {
		rules[kind] = map[discovery.Mode]discovery.Rule{}
		for _, mode := range []discovery.Mode{discovery.Search, discovery.Recommendation} {
			rules[kind][mode] = discovery.Rule{Version: "e2e-rules", BM25Scale: 1, CosineFloor: 0, MinRelevance: .01, Threshold: .01, HalfLifeMS: 86400000}
		}
	}
	rulesPath := filepath.Join(s.logs, "rules.json")
	writeJSON(t, rulesPath, rules)
	ports := map[string]int{}
	for _, name := range []string{"SORT_RPC_PORT", "FEED_RPC_PORT", "ITEM_RPC_PORT", "API_PORT"} {
		ports[name] = freePort(t)
	}
	s.url = fmt.Sprintf("http://127.0.0.1:%d", ports["API_PORT"])
	for k, v := range map[string]string{
		"ENABLE_NEED_SEARCH": "true", "ENABLE_CONSOLE_V2": "true", "ENABLE_REPLAY_LOG": "true", "ENABLE_COMMISSION_INDEX": "true", "ENABLE_COMMISSION_DISCOVERY_API": "true",
		"DISCOVERY_TAXONOMY_PATH": taxPath, "DISCOVERY_RULES_PATH": rulesPath, "AGENT_DISCOVERY_INDEX": s.agentIndex,
		"COMMISSION_INDEX_NAME": s.commissionIndex, "COMMISSION_INDEX_ALIAS": s.commissionIndex + "-read",
		"COMMISSION_SOURCE_SERVICE": "DiscoveryE2ECommission", "COMMISSION_ORDER_SOURCE_SERVICE": "DiscoveryE2EOrder",
		"ENABLE_COMMISSION_AGENT_ID_WHITELIST": "false", "COMMISSION_INTEGRATION_MODE": "false",
		"EMBEDDING_PROVIDER": "openai", "EMBEDDING_MODEL": "discovery-e2e", "EMBEDDING_BASE_URL": embedding.URL, "EMBEDDING_API_KEY": "test-only",
		"CONSOLE_V2_BOOTSTRAP_SECRET": "discovery-e2e-test-only", "CONSOLE_V2_OTP_PEPPER": "discovery-e2e-test-only", "CONSOLE_V2_PUBLIC_URL": s.url,
		"ENABLE_HOT_RECALL": "false", "ENABLE_NEW_RECALL": "false", "ENABLE_NEW_UGC_RECALL": "false", "ENABLE_SWING_I2I_RECALL": "false",
		"DISABLE_DEDUP_IN_TEST": "false", "MONITOR_ENABLED": "false", "LR_RANKER_ENABLED": "false",
	} {
		t.Setenv(k, v)
	}
	for k, v := range ports {
		t.Setenv(k, strconv.Itoa(v))
	}
	s.seed(t)
	s.startCatalogue(t)
	for _, name := range []string{"sort", "item", "feed"} {
		s.startProcess(t, name, ports[map[string]string{"sort": "SORT_RPC_PORT", "item": "ITEM_RPC_PORT", "feed": "FEED_RPC_PORT", "api": "API_PORT"}[name]])
	}
	// Listening precedes Kitex registration. Avoid caching an empty resolver
	// result in the gateway while downstream services are still starting.
	require.Eventually(t, func() bool {
		for _, service := range []string{"SortService", "ItemService", "FeedService", "DiscoveryE2ECommission", "DiscoveryE2EOrder"} {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			result, err := etcdClient.Get(ctx, "kitex/registry-etcd/"+service+"/", clientv3.WithPrefix())
			cancel()
			if err != nil || len(result.Kvs) != 1 {
				return false
			}
		}
		return true
	}, 20*time.Second, 100*time.Millisecond, "RPC registration readiness")
	s.startProcess(t, "api", ports["API_PORT"])
	ids, err := idgen.NewManagedGenerator(context.Background(), idgen.ManagedGeneratorConfig{Endpoints: strings.Split(cfg.EtcdAddr, ","), WorkerPrefix: cfg.IDWorkerPrefix, ServiceName: "discovery-e2e-replay", LeaseTTLSecond: cfg.IDWorkerLeaseTTL, EpochMS: cfg.IDSnowflakeEpoch})
	require.NoError(t, err)
	workerCtx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); consumer.NewReplayConsumer(ids).Start(workerCtx) }()
	t.Cleanup(func() {
		stop()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("replay consumer did not stop")
		}
		require.NoError(t, ids.Close(context.Background()))
	})
	require.Eventually(t, func() bool {
		status, _, err := s.request("GET", "/api/v2/taxonomy/search?query=landing", s.token, "", nil)
		return err == nil && status == 200
	}, 20*time.Second, 100*time.Millisecond, "API/Sort discovery readiness; inspect process logs")
	require.Eventually(t, func() bool {
		status, _, err := s.request("POST", "/api/v2/discovery/search", s.token, "", discovery.Request{SourceKinds: []discovery.Kind{discovery.Agent}})
		return err == nil && status == 400
	}, 20*time.Second, 100*time.Millisecond, "API/Feed/Sort readiness via missing-query validation")
	return s
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	p := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close())
	return p
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	b, err := json.Marshal(value)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, b, 0600))
}

func (s *stack) startProcess(t *testing.T, name string, port int) {
	t.Helper()
	f, err := os.Create(filepath.Join(s.logs, name+".log"))
	require.NoError(t, err)
	cmd := exec.Command(filepath.Join(s.root, "build", name))
	cmd.Dir = s.root
	cmd.Env = os.Environ()
	cmd.Stdout = f
	cmd.Stderr = f
	require.NoError(t, cmd.Start())
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
		_ = f.Close()
	})
	require.Eventually(t, func() bool {
		select {
		case <-done:
			t.Fatalf("%s exited during startup; inspect %s", name, filepath.Join(s.logs, name+".log"))
		default:
		}
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
		if err != nil {
			return false
		}
		_ = c.Close()
		return true
	}, 20*time.Second, 100*time.Millisecond, "%s startup; inspect %s", name, filepath.Join(s.logs, name+".log"))
}

type envelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

func (s *stack) request(method, path, token, key string, body any) (int, envelope, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return 0, envelope{}, err
	}
	req, err := http.NewRequest(method, s.url+path, bytes.NewReader(raw))
	if err != nil {
		return 0, envelope{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return 0, envelope{}, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, envelope{}, err
	}
	var out envelope
	err = json.Unmarshal(b, &out)
	return resp.StatusCode, out, err
}

func (s *stack) call(t *testing.T, method, path, token, key string, body any, status int) json.RawMessage {
	t.Helper()
	got, out, err := s.request(method, path, token, key, body)
	require.NoError(t, err)
	require.Equal(t, status, got, "%s %s: %s", method, path, out.Msg)
	if status == 200 {
		require.Zero(t, out.Code, out.Msg)
	}
	return out.Data
}

func decode[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var v T
	require.NoError(t, json.Unmarshal(raw, &v))
	return v
}
