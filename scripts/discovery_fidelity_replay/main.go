// Capture and replay complete Discovery recall pools. Production connections are
// read-only; forward-feature repairs target an isolated loopback Redis instance.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	elasticsearch "github.com/elastic/go-elasticsearch/v8"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/es"
	"eigenflux_server/pkg/recall"
	"eigenflux_server/pkg/recallsource"
	"eigenflux_server/rpc/sort/discovery"
	"eigenflux_server/rpc/sort/discovery/needembedding"
	"eigenflux_server/rpc/sort/discoverylr"
	"eigenflux_server/rpc/sort/legacy"
	"github.com/redis/go-redis/v9"
	"github.com/redis/go-redis/v9/maintnotifications"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	glog "gorm.io/gorm/logger"
)

type readOnlyRedis struct{}

func (readOnlyRedis) DialHook(next redis.DialHook) redis.DialHook { return next }
func allowed(cmd redis.Cmder) error {
	switch strings.ToLower(cmd.Name()) {
	case "client":
		if len(cmd.Args()) > 1 && strings.ToLower(fmt.Sprint(cmd.Args()[1])) == "setinfo" {
			return nil
		}
	case "hello", "auth", "select", "ping", "get", "mget", "exists", "zrange", "zrangebyscore", "zrevrange", "zrevrangebyscore", "sismember", "smismember", "smembers":
		return nil
	}
	return fmt.Errorf("production Redis command denied: %s", cmd.Name())
}
func (readOnlyRedis) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, c redis.Cmder) error {
		if err := allowed(c); err != nil {
			return err
		}
		return next(ctx, c)
	}
}
func (readOnlyRedis) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cs []redis.Cmder) error {
		for _, c := range cs {
			if err := allowed(c); err != nil {
				return err
			}
		}
		return next(ctx, cs)
	}
}
func must(err error) {
	if err != nil {
		panic(err)
	}
}
func save(path string, v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	must(err)
	f, err := os.CreateTemp(filepath.Dir(path), ".fidelity-snapshot-*")
	must(err)
	defer os.Remove(f.Name())
	_, err = f.Write(b)
	if err != nil {
		f.Close()
		must(err)
	}
	must(f.Close())
	must(os.Rename(f.Name(), path))
}
func engine(cfg *config.Config, dir string, s *snapshot, source discovery.Sources) (*discovery.Engine, func()) {
	b, err := os.ReadFile(filepath.Join(dir, "rules.json"))
	must(err)
	var rules discovery.Rules
	must(json.Unmarshal(b, &rules))
	must(rules.Validate())
	m := discoverylr.New(cfg.DiscoveryLREnabled, filepath.Join(dir, "model.json"), 24*time.Hour)
	policy := legacy.NewDiscoveryReplayPolicies(cfg, filepath.Join(dir, "rerank.yaml"), time.UnixMilli(s.At), s.Classes, s.Claims)
	return &discovery.Engine{Sources: source, Rules: rules, Learned: m, Policies: policy.DiscoveryPolicies, SourceLimits: policy.DiscoverySourceLimits()}, m.Close
}
func main() {
	mode := flag.String("mode", "capture", "capture or replay")
	dir := flag.String("dir", "", "private snapshot directory with frozen rules/model/rerank")
	limit := flag.Int("agents", 100, "maximum recent Agents (1-200)")
	flag.Parse()
	if *dir == "" || *limit < 1 || *limit > 200 {
		panic("provide snapshot directory and 1-200 Agents")
	}
	must(os.MkdirAll(*dir, 0700))
	cfg := config.Load()
	if *mode == "compare" {
		counterfactual(cfg, *dir)
		return
	}
	if *mode == "replay" {
		replayAll(cfg, *dir)
		return
	}
	if *mode != "capture" && *mode != "evidence" && *mode != "history" && *mode != "observed" {
		panic("invalid mode")
	}
	// Require server-enforced read-only PG sessions, not a caller promise.
	db, err := gorm.Open(postgres.New(postgres.Config{DSN: cfg.PgDSN, PreferSimpleProtocol: true}), &gorm.Config{DisableAutomaticPing: true, Logger: glog.Default.LogMode(glog.Silent)})
	must(err)
	// The provider proxy can stall pgx's comment-only idle-connection ping.
	// Diagnostics use bounded short-lived read-only sessions instead.
	sqlDB, err := db.DB()
	must(err)
	sqlDB.SetMaxOpenConns(2)
	sqlDB.SetMaxIdleConns(0)
	defer sqlDB.Close()
	var ro string
	must(db.Raw("SHOW default_transaction_read_only").Scan(&ro).Error)
	if ro != "on" {
		panic("PostgreSQL must be read-only")
	}
	if *mode == "observed" {
		exportObservedFeedback(db, *dir)
		return
	}
	if *mode == "history" {
		exportHistory(db, *dir)
		return
	}
	if *mode == "evidence" {
		exportEvidence(db, *dir)
		return
	}
	host, _, err := net.SplitHostPort(cfg.RedisAddr)
	must(err)
	if host != "127.0.0.1" {
		panic("scratch Redis must be loopback")
	}
	scratch := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr})
	defer scratch.Close()
	remote := redis.NewClient(&redis.Options{Addr: os.Getenv("FROZEN_REMOTE_REDIS_ADDR"), Password: cfg.RedisPassword, ReadTimeout: 30 * time.Second, WriteTimeout: 10 * time.Second, PoolSize: 6, MaxRetries: -1, Protocol: 2, MaintNotificationsConfig: &maintnotifications.Config{Mode: maintnotifications.ModeDisabled}})
	remote.AddHook(readOnlyRedis{})
	defer remote.Close()
	// The offline client crosses an SSH tunnel; use a bounded transport suited
	// to that latency rather than the production in-VPC five-second header budget.
	es.Client, err = elasticsearch.NewClient(elasticsearch.Config{Addresses: []string{os.Getenv("ES_URL")}, Username: os.Getenv("ES_USERNAME"), Password: os.Getenv("ES_PASSWORD"), DisableRetry: true, Transport: &http.Transport{MaxIdleConnsPerHost: 6, ResponseHeaderTimeout: 60 * time.Second}})
	must(err)
	// All contexts from the most recent broadcast recommendation for each Agent;
	// all channels are executed afresh, including candidates never delivered.
	var rows []struct {
		AgentID  int64
		Contexts string
	}
	cohortPath := filepath.Join(*dir, "cohort.json")
	if b, e := os.ReadFile(cohortPath); e == nil {
		must(json.Unmarshal(b, &rows))
	} else {
		must(db.Raw(`WITH recent AS (
 SELECT agent_id,served_at,agent_features FROM replay_logs
 WHERE pipeline_version='need_search_v1' AND request_mode='recommendation'
 AND source_kind='broadcast' AND served_at>extract(epoch from now()-interval '24 hours')*1000
 ORDER BY served_at DESC LIMIT 20000), last AS (
 SELECT DISTINCT ON(agent_id) agent_id,served_at,agent_features FROM recent ORDER BY agent_id,served_at DESC)
 SELECT agent_id,(agent_features->'search_context'->'contexts')::text AS contexts FROM last ORDER BY served_at DESC LIMIT ?`, *limit).Scan(&rows).Error)
		save(cohortPath, rows)
	}

	vectors := needembedding.New(cfg, db, remote)
	reader := recall.NewRedisRecallReader(remote, cfg.RecallRedisNamespace)
	live := &discovery.Source{DB: db, Redis: remote, RecallNamespace: cfg.RecallRedisNamespace, BlockedAuthorEmails: cfg.BlockedAgentEmails,
		FriendFeedMaxAuthors: cfg.FriendFeedMaxAuthors, FriendFeedWindowHours: cfg.FriendFeedWindowHours, FriendFeedMaxItems: cfg.FriendFeedMaxItems,
		SwingRecall:      recallsource.NewSwingI2IRecallSource(reader, recall.NewSurfaceHistoryStore(scratch, cfg.RecallRedisNamespace), remote, cfg.SwingI2IRecallSeeds, cfg.SwingI2IRecallK),
		DisabledChannels: map[string]bool{"friend": !cfg.FriendFeedEnabled, "swing_i2i": !cfg.EnableSwingI2IRecall, "hot_recall": !cfg.EnableHotRecall, "new_recall": !cfg.EnableNewRecall, "new_ugc_recall": !cfg.EnableNewUGCRecall}}
	pools := map[string][]discovery.Document{}
	for _, ch := range []string{"hot_recall", "new_recall", "new_ugc_recall"} {
		n := 20
		if ch == "new_ugc_recall" {
			n = 10
		}
		pool, err := live.Recall(context.Background(), discovery.Context{}, discovery.Broadcast, ch, n)
		must(err)
		pools[ch] = pool
	}
	hydration := *live
	hydration.Redis = scratch
	for _, row := range rows {
		file := filepath.Join(*dir, fmt.Sprintf("agent-%d.json", row.AgentID))
		if b, e := os.ReadFile(file); e == nil {
			var old snapshot
			if json.Unmarshal(b, &old) == nil && old.CaptureError == "" && len(old.Hydration) > 0 {
				fmt.Printf("agent %d: valid snapshot retained\n", row.AgentID)
				continue
			}
		}
		key := recall.SurfaceHistoryKey(cfg.RecallRedisNamespace, row.AgentID)
		history, err := remote.ZRevRangeWithScores(context.Background(), key, 0, -1).Result()
		must(err)
		must(scratch.Del(context.Background(), key).Err())
		if len(history) > 0 {
			must(scratch.ZAdd(context.Background(), key, history...).Err())
		}
		s := &snapshot{Owner: row.AgentID, At: time.Now().UnixMilli(), Channels: map[string]channel{}, Hydrated: map[string]discovery.Document{}, Seen: map[string]bool{}, Classes: map[int64]string{}, Claims: map[int64]bool{}, Vectors: map[int64][]float32{}}
		var contexts []discovery.Context
		must(json.Unmarshal([]byte(row.Contexts), &contexts))
		for _, c := range contexts {
			has := false
			for _, k := range c.Kinds {
				has = has || k == discovery.Broadcast
			}
			if !has {
				continue
			}
			c.Kinds = []discovery.Kind{discovery.Broadcast}
			if c.CapturedNeed != nil && c.CapturedNeed.InputID > 0 && c.QueryAnalysis != nil {
				v, e := vectors.Read(context.Background(), c.QueryAnalysis.Normalized)
				if e == nil {
					c.Vector = v
				} else if !strings.Contains(e.Error(), "pending") {
					s.CaptureError = "Need vector unavailable"
				}
			}
			s.Contexts = append(s.Contexts, c)
			s.Vectors[c.ID] = c.Vector
		}
		recorder := &recordedSource{pools: pools, live: live, hydrate: &hydration, snap: s}
		recorder.facts = func(ctx context.Context, docs []discovery.Document) error {
			ids := []int64{}
			authors := []int64{}
			for _, d := range docs {
				ids = append(ids, d.Ref.ID)
				authors = append(authors, d.AuthorID)
			}
			var people []struct {
				AgentID int64
				Email   string
			}
			if len(authors) > 0 {
				if err := db.WithContext(ctx).Table("agents").Select("agent_id,email").Where("agent_id IN ?", authors).Scan(&people).Error; err != nil {
					return err
				}
			}
			pgc := map[int64]bool{}
			for _, p := range people {
				pgc[p.AgentID] = config.EmailMatchesAnySuffix(p.Email, cfg.PGCEmailSuffixes)
			}
			pipe := remote.Pipeline()
			claims := map[int64]*redis.IntCmd{}
			for _, id := range ids {
				claims[id] = pipe.Exists(ctx, fmt.Sprintf("sort:inject:claim:%d", id))
			}
			if _, err := pipe.Exec(ctx); err != nil {
				return err
			}
			for _, d := range docs {
				class := "ugc"
				if pgc[d.AuthorID] {
					class = "pgc"
				}
				s.Classes[d.Ref.ID] = class
				s.Claims[d.Ref.ID] = claims[d.Ref.ID].Val() > 0
			}
			return nil
		}
		e, close := engine(cfg, *dir, s, recorder)
		work, cancel := context.WithTimeout(context.Background(), 180*time.Second)
		result, err := e.ReplayContexts(work, s.Owner, s.Contexts, []discovery.Kind{discovery.Broadcast}, 20, s.At)
		cancel()
		close()
		s.Before = result
		for _, c := range s.Channels {
			if c.Error != "" {
				s.CaptureError = "incomplete recall: " + c.Name
			}
		}
		for _, reason := range result.PartialReasons {
			if strings.Contains(reason, "unavailable") {
				s.CaptureError = "partial execution: " + reason
			}
		}
		if err != nil {
			s.CaptureError = err.Error()
		}
		save(filepath.Join(*dir, fmt.Sprintf("agent-%d.json", s.Owner)), s)
		fmt.Printf("agent %d: channels=%d hydrated=%d delivered=%d error=%t\n", s.Owner, len(s.Channels), len(s.Hydrated), len(result.Candidates), s.CaptureError != "")
	}
}
func replayAll(cfg *config.Config, dir string) {
	snapshots := loadSnapshots(dir)
	for _, entry := range snapshots {
		s := *entry
		e, close := engine(cfg, dir, &s, newFrozenSource(&s))
		got, err := e.ReplayContexts(context.Background(), s.Owner, s.Contexts, []discovery.Kind{discovery.Broadcast}, 20, s.At)
		close()
		must(err)
		a, _ := json.Marshal(got)
		b, _ := json.Marshal(s.Before)
		if string(a) != string(b) {
			panic(fmt.Sprintf("snapshot replay mismatch for %d", s.Owner))
		}
		fmt.Printf("agent %d: exact baseline reproduction (%d candidates)\n", s.Owner, len(got.Candidates))
	}
}
