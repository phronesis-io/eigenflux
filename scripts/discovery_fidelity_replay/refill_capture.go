package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"eigenflux_server/pkg/bloomfilter"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/es"
	"eigenflux_server/pkg/featureindex"
	"eigenflux_server/pkg/need"
	"eigenflux_server/pkg/recall"
	"eigenflux_server/pkg/recallsource"
	sortdal "eigenflux_server/rpc/sort/dal"
	"eigenflux_server/rpc/sort/discovery"
	"eigenflux_server/rpc/sort/discovery/needembedding"
	"github.com/redis/go-redis/v9"
	"github.com/redis/go-redis/v9/maintnotifications"
	"gorm.io/gorm"
)

const captureHistoryScript = `local n = redis.call('SCARD', KEYS[1])
if n > tonumber(ARGV[1]) then return false end
return redis.call('SMEMBERS', KEYS[1])`
const refillCaptureDepth = 640
const refillCaptureFormat = "consumed-refill-v4"

type refillSnapshot struct {
	CaptureFormat string
	snapshot
	History         []int64
	Confirmed       []int64
	HistoryComplete bool
	HistoryError    bool
	RefillPools     map[int64][]storedDocument
	Truncated       map[int64]bool
	CaptureMS       int64
}

type localIDs int64

func (i *localIDs) NextID() (int64, error) { *i++; return int64(*i), nil }

type readVectors struct{ cache *needembedding.Cache }

func (v readVectors) Lookup(ctx context.Context, _ int64, text, processor string) ([]float32, error) {
	if processor != v.cache.Profile.Processor {
		return nil, fmt.Errorf("vector generation mismatch")
	}
	return v.cache.Read(ctx, text)
}

type contextOnlySource struct{ *discovery.Source }

func (s contextOnlySource) Recall(context.Context, discovery.Context, discovery.Kind, string, int) ([]discovery.Document, error) {
	return nil, nil
}
func (s contextOnlySource) Hydrate(context.Context, int64, discovery.Mode, []discovery.Document) ([]discovery.Document, error) {
	return nil, nil
}
func (s contextOnlySource) Seen(context.Context, int64, []discovery.Document) (map[string]bool, error) {
	return map[string]bool{}, nil
}

func captureRefill(cfg *config.Config, dir string, db *gorm.DB, remote, scratch *redis.Client) {
	var cohort []struct{ AgentID int64 }
	b, err := os.ReadFile(filepath.Join(dir, "cohort.json"))
	must(err)
	must(json.Unmarshal(b, &cohort))
	if len(cohort) != 225 {
		panic("refill cohort must contain exactly 225 fixed Agents")
	}
	seen := map[int64]bool{}
	for _, a := range cohort {
		if a.AgentID <= 0 || seen[a.AgentID] {
			panic("invalid fixed cohort")
		}
		seen[a.AgentID] = true
	}
	if host := os.Getenv("REFILL_CAPTURE_SSH_HOST"); host != "" {
		captureSlim, err = startSlimPool(host)
		must(err)
		defer captureSlim.Close()
	}
	vectors := needembedding.New(cfg, db, remote)
	reader := recall.NewRedisRecallReader(remote, cfg.RecallRedisNamespace)
	live := &discovery.Source{DB: db, Redis: remote, RecallNamespace: cfg.RecallRedisNamespace, BlockedAuthorEmails: cfg.BlockedAgentEmails,
		FriendFeedMaxAuthors: cfg.FriendFeedMaxAuthors, FriendFeedWindowHours: cfg.FriendFeedWindowHours, FriendFeedMaxItems: cfg.FriendFeedMaxItems,
		SwingRecall:      recallsource.NewSwingI2IRecallSource(reader, recall.NewSurfaceHistoryStore(scratch, cfg.RecallRedisNamespace), remote, cfg.SwingI2IRecallSeeds, cfg.SwingI2IRecallK),
		DisabledChannels: map[string]bool{"friend": !cfg.FriendFeedEnabled, "swing_i2i": !cfg.EnableSwingI2IRecall, "hot_recall": !cfg.EnableHotRecall, "new_recall": !cfg.EnableNewRecall, "new_ugc_recall": !cfg.EnableNewUGCRecall}}

	workers := 6
	if value := os.Getenv("REFILL_CAPTURE_WORKERS"); value != "" {
		workers, err = strconv.Atoi(value)
		must(err)
	}
	if workers < 1 || workers > 12 {
		panic("capture workers must be in [1,12]")
	}
	jobs := make(chan int)
	var wg sync.WaitGroup
	for worker := 1; worker <= workers; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			options := *scratch.Options()
			options.MaintNotificationsConfig = &maintnotifications.Config{Mode: maintnotifications.ModeDisabled}
			options.DB = worker
			local := redis.NewClient(&options)
			defer local.Close()
			copy := *live
			copy.SwingRecall = recallsource.NewSwingI2IRecallSource(reader, recall.NewSurfaceHistoryStore(local, cfg.RecallRedisNamespace), remote, cfg.SwingI2IRecallSeeds, cfg.SwingI2IRecallK)
			hydration := copy
			hydration.Redis = local
			live, scratch := &copy, local
			for n := range jobs {
				a := cohort[n]
				file := filepath.Join(dir, fmt.Sprintf("refill-agent-%d.json", a.AgentID))
				if b, err := os.ReadFile(file); err == nil {
					var old refillSnapshot
					if json.Unmarshal(b, &old) == nil && old.CaptureError == "" && old.CaptureFormat == refillCaptureFormat {
						fmt.Printf("capture %d/225 retained\n", n+1)
						continue
					}
				}
				start := time.Now()
				ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
				s := refillSnapshot{CaptureFormat: refillCaptureFormat, snapshot: snapshot{Owner: a.AgentID, At: start.UnixMilli(), Channels: map[string]channel{}, Hydrated: map[string]discovery.Document{}, Seen: map[string]bool{}, Classes: map[int64]string{}, Claims: map[int64]bool{}, Vectors: map[int64][]float32{}}, RefillPools: map[int64][]storedDocument{}, Truncated: map[int64]bool{}}
				err := captureRefillAgent(ctx, cfg, dir, db, remote, scratch, live, &hydration, vectors, &s)
				cancel()
				s.CaptureMS = time.Since(start).Milliseconds()
				if err != nil {
					s.CaptureError = err.Error()
				}
				save(file, s)
				fmt.Printf("capture %d/225 contexts=%d hydrated=%d ms=%d error=%t\n", n+1, len(s.Contexts), len(s.Hydrated), s.CaptureMS, err != nil)
			}
		}(worker)
	}
	for i := range cohort {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

}
func captureRefillAgent(ctx context.Context, cfg *config.Config, dir string, db *gorm.DB, remote, scratch *redis.Client, live, hydration *discovery.Source, vectors *needembedding.Cache, s *refillSnapshot) error {
	ids := localIDs(100)
	compiler := &discovery.Compiler{NeedVectors: readVectors{vectors}, EmbeddingVersion: vectors.Generation()}
	e, close := engine(cfg, dir, &s.snapshot, contextOnlySource{live})
	defer close()
	e.Compiler = compiler
	e.IDs = &ids
	e.Needs = need.Store{DB: db}
	e.FriendFeedEnabled = cfg.FriendFeedEnabled
	contexts, err := e.Execute(ctx, s.Owner, discovery.Request{SourceKinds: []discovery.Kind{discovery.Broadcast}, Limit: 20}, discovery.Recommendation, s.At)
	if err != nil {
		return err
	}
	s.Contexts = contexts.Contexts
	if os.Getenv("REFILL_CAPTURE_VERBOSE") == "1" {
		fmt.Fprintln(os.Stderr, "phase contexts complete")
	}
	for _, c := range s.Contexts {
		s.Vectors[c.ID] = c.Vector
		for _, w := range c.Warnings {
			if w == "embedding_unavailable" {
				return fmt.Errorf("unavailable context embedding")
			}
		}
	}
	historyKey := recall.SurfaceHistoryKey(cfg.RecallRedisNamespace, s.Owner)
	history, err := remote.ZRevRangeWithScores(ctx, historyKey, 0, 99).Result()
	if err != nil {
		return err
	}
	if err = scratch.Del(ctx, historyKey).Err(); err != nil {
		return err
	}
	if len(history) > 0 {
		if err = scratch.ZAdd(ctx, historyKey, history...).Err(); err != nil {
			return err
		}
	}
	if captureSlim != nil {
		history, err := captureSlim.consumption(ctx, s.Owner, "history", nil)
		if err != nil {
			return err
		}
		s.History, s.Confirmed, s.HistoryComplete, s.HistoryError = history.History, history.Confirmed, history.Complete, history.Unavailable
	} else {
		raw, err := remote.EvalRO(ctx, captureHistoryScript, []string{fmt.Sprintf("impr:agent:%d:groups", s.Owner)}, 65536).StringSlice()
		if err == redis.Nil {
			s.HistoryError = true
		} else if err != nil {
			return err
		}
		for _, v := range raw {
			id, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return err
			}
			s.History = append(s.History, id)
		}
		sort.Slice(s.History, func(i, j int) bool { return s.History[i] > s.History[j] })
		if len(s.History) > discovery.MaxRefillGroups {
			s.History = s.History[:discovery.MaxRefillGroups]
		}
		s.Confirmed, s.HistoryComplete, err = live.ConsumedGroups(ctx, s.Owner)
		if err != nil && err != redis.Nil {
			return err
		}
	}
	if os.Getenv("REFILL_CAPTURE_VERBOSE") == "1" {
		fmt.Fprintln(os.Stderr, "phase history complete")
	}
	union := map[string]discovery.Document{}
	shared := map[string][]discovery.Document{}
	for _, c := range s.Contexts {
		channels := []string{"lexical", "hot_recall", "new_recall", "new_ugc_recall", "swing_i2i"}
		if len(c.Vector) > 0 {
			channels = append(channels, "dense")
		}
		if c.Origin == "baseline" {
			channels = []string{"hot_recall", "new_recall", "swing_i2i"}
		}
		if c.Origin == "friend" {
			channels = []string{"friend"}
		}
		if c.UnverifiedNeedReason != "" {
			continue
		}
		lexicalIDs := []int64{}
		for _, ch := range channels {
			limit := 80
			switch ch {
			case "hot_recall", "new_recall":
				limit = 20
			case "new_ugc_recall":
				limit = 10
			case "swing_i2i", "friend":
				limit = 200
			}
			docs, cached := shared[ch]
			var err error
			if !cached {
				if captureSlim != nil && (ch == "lexical" || ch == "dense") {
					docs, err = captureSlim.Query(ctx, c, ch, limit, nil, nil, s.At)
				} else {
					docs, err = live.Recall(ctx, c, discovery.Broadcast, ch, limit)
				}
				if err == nil && (strings.HasSuffix(ch, "recall") || ch == "swing_i2i") {
					shared[ch] = docs
				}
			}
			docs = append([]discovery.Document(nil), docs...)
			for i := range docs {
				docs[i].NeedExclusionText = len(c.Filters.ExcludeTerms) > 0
			}
			if err != nil {
				return fmt.Errorf("capture %s: %w", ch, err)
			}
			call := channel{c.ID, discovery.Broadcast, ch, limit, storeDocs(docs), ""}
			s.Channels[channelKey(c, discovery.Broadcast, ch)] = call
			for _, d := range docs {
				mergeCaptureDocument(union, d)
				if ch == "lexical" {
					lexicalIDs = append(lexicalIDs, d.Ref.ID)
				}
			}
		}
		if c.Origin == "baseline" || c.Origin == "friend" || s.HistoryError {
			continue
		}
		docs, truncated, err := captureDeepLexical(ctx, c, s.Confirmed, lexicalIDs, s.At)
		if err != nil {
			return err
		}
		for i := range docs {
			docs[i].NeedExclusionText = len(c.Filters.ExcludeTerms) > 0
		}
		s.RefillPools[c.ID] = storeDocs(docs)
		s.Truncated[c.ID] = truncated
		for _, d := range docs {
			mergeCaptureDocument(union, d)
		}
	}
	if os.Getenv("REFILL_CAPTURE_VERBOSE") == "1" {
		fmt.Fprintln(os.Stderr, "phase recall complete")
	}
	all := make([]discovery.Document, 0, len(union))
	for _, d := range union {
		all = append(all, d)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Ref.ID < all[j].Ref.ID })
	for i := 0; i < len(all); i += 1000 {
		end := i + 1000
		if end > len(all) {
			end = len(all)
		}
		batch := all[i:end]
		// Preserve live forward values (including version mismatches); only missing
		// or expired read-through repairs are allowed in isolated local Redis.
		values, err := captureForward(ctx, remote, batch)
		if err != nil {
			return err
		}
		local := scratch.Pipeline()
		for key, value := range values {
			local.Del(ctx, key)
			if len(value) > 0 {
				local.HSet(ctx, key, value)
			}
		}
		if _, err = local.Exec(ctx); err != nil {
			return err
		}
		fresh, err := hydration.Hydrate(ctx, s.Owner, discovery.Recommendation, batch)
		if err != nil {
			return err
		}
		// Fill source text for authority filtering without repairing stale versions.
		for j := range fresh {
			p := union[fresh[j].Ref.Key()]
			if p.Version != "" && p.Version == fresh[j].Version {
				fresh[j].Text = p.Text
				fresh[j].Preview = p.Preview
			}
		}
		seen, err := captureSeen(ctx, remote, s.Owner, fresh)
		if err != nil {
			return err
		}
		authors := []int64{}
		itemIDs := []int64{}
		for _, d := range fresh {
			authors = append(authors, d.AuthorID)
			itemIDs = append(itemIDs, d.Ref.ID)
			s.Hydrated[d.Ref.Key()] = d
			s.Seen[d.Ref.Key()] = seen[d.Ref.Key()]
		}
		var people []struct {
			AgentID int64
			Email   string
		}
		if len(authors) > 0 {
			if err = db.WithContext(ctx).Table("agents").Select("agent_id,email").Where("agent_id IN ?", authors).Scan(&people).Error; err != nil {
				return err
			}
		}
		pgc := map[int64]bool{}
		for _, p := range people {
			pgc[p.AgentID] = config.EmailMatchesAnySuffix(p.Email, cfg.PGCEmailSuffixes)
		}
		claims, err := captureClaims(ctx, remote, itemIDs)
		if err != nil {
			return err
		}
		for _, d := range fresh {
			lane := "ugc"
			if pgc[d.AuthorID] {
				lane = "pgc"
			}
			s.Classes[d.Ref.ID] = lane
			s.Claims[d.Ref.ID] = claims[d.Ref.ID]
		}
		s.Inventory = append(s.Inventory, storeDocs(fresh)...)
	}
	source := newSequentialSource(s)
	baseline, done := engine(cfg, dir, &s.snapshot, source)
	defer done()
	s.Before, err = baseline.ReplayContexts(ctx, s.Owner, s.Contexts, []discovery.Kind{discovery.Broadcast}, 20, s.At)
	return err
}

func captureDeepLexical(ctx context.Context, c discovery.Context, groups, ids []int64, now int64) ([]discovery.Document, bool, error) {
	if captureSlim != nil {
		docs, err := captureSlim.Query(ctx, c, "lexical", refillCaptureDepth, groups, ids, now)
		return docs, len(docs) == refillCaptureDepth, err
	}
	q, err := discovery.ReplayRecallQuery(c, groups, ids, now)
	if err != nil {
		return nil, false, err
	}
	q["size"] = refillCaptureDepth
	q["_source"] = []string{"id", "author_agent_id", "content", "summary", "lang", "retrieval_slots", "group_id"}
	b, err := json.Marshal(q)
	if err != nil {
		return nil, false, err
	}
	resp, err := es.Client.Search(es.Client.Search.WithContext(ctx), es.Client.Search.WithIndex(es.ReadIndexPattern), es.Client.Search.WithBody(bytes.NewReader(b)))
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if resp.IsError() {
		return nil, false, fmt.Errorf("capture ES status %d", resp.StatusCode)
	}
	var env struct {
		TimedOut bool `json:"timed_out"`
		Shards   struct {
			Failed int `json:"failed"`
		} `json:"_shards"`
		Hits struct {
			Hits []struct {
				Score  float64         `json:"_score"`
				Source json.RawMessage `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&env); err != nil {
		return nil, false, err
	}
	if env.TimedOut || env.Shards.Failed > 0 {
		return nil, false, fmt.Errorf("incomplete deep lexical capture")
	}
	out := []discovery.Document{}
	for _, h := range env.Hits.Hits {
		var item sortdal.Item
		if err = json.Unmarshal(h.Source, &item); err != nil {
			return nil, false, err
		}
		slots := item.RetrievalSlots
		if item.Lang != "" {
			slots.Lang = []string{item.Lang}
		}
		text := item.Content + "\n" + item.Summary
		out = append(out, discovery.Document{Ref: discovery.SourceRef{Type: discovery.Broadcast, ID: item.ID}, AuthorID: item.AuthorAgentID, GroupID: item.GroupID, Text: text, Preview: item.Summary, Slots: slots, Lexical: h.Score, Version: featureindex.BroadcastContentHash(item.ID, item.AuthorAgentID, text, slots)})
	}
	return out, len(out) == refillCaptureDepth, nil
}

func allowedConsumptionVerification(args []any) bool {
	_, script := discovery.ReplayConsumptionReadScripts()
	if len(args) < 33 || fmt.Sprint(args[1]) != script || fmt.Sprint(args[2]) != "30" || len(args) > 33+discovery.MaxRefillGroups {
		return false
	}
	for _, key := range args[3:33] {
		if !strings.HasPrefix(fmt.Sprint(key), "bf:global:") {
			return false
		}
	}
	return true
}

// The capture uses the same rolling authority as Source.Seen, but sends each
// member once across the remote link. It does not change online dedup behavior.
func captureSeen(ctx context.Context, remote *redis.Client, owner int64, docs []discovery.Document) (map[string]bool, error) {
	_, script := discovery.ReplayConsumptionReadScripts()
	keys := make([]string, bloomfilter.BloomFilterLookbackDays)
	now := time.Now()
	for i := range keys {
		keys[i] = bloomfilter.GetKeyForDate(now.AddDate(0, 0, -i))
	}
	args := []any{}
	unique := map[int64]bool{}
	prefix := strconv.FormatInt(owner, 10) + ":"
	for _, d := range docs {
		if d.GroupID > 0 && !unique[d.GroupID] {
			args = append(args, prefix+strconv.FormatInt(d.GroupID, 10))
			unique[d.GroupID] = true
		}
	}
	hits := map[string]bool{}
	if captureSlim != nil {
		groups := make([]int64, 0, len(unique))
		for id := range unique {
			groups = append(groups, id)
		}
		result, err := captureSlim.consumption(ctx, owner, "seen", groups)
		if err != nil {
			return nil, err
		}
		for _, id := range result.Confirmed {
			hits[prefix+strconv.FormatInt(id, 10)] = true
		}
	} else if len(args) > 0 {
		values, err := remote.EvalRO(ctx, script, keys, args...).StringSlice()
		if err != nil {
			return nil, err
		}
		for _, v := range values {
			hits[v] = true
		}
	}
	out := map[string]bool{}
	for _, d := range docs {
		out[d.Ref.Key()] = hits[prefix+strconv.FormatInt(d.GroupID, 10)]
	}
	return out, nil
}

func mergeCaptureDocument(union map[string]discovery.Document, d discovery.Document) {
	prior, ok := union[d.Ref.Key()]
	if !ok {
		union[d.Ref.Key()] = d
		return
	}
	exclusion := prior.NeedExclusionText || d.NeedExclusionText
	if prior.Version == "" {
		prior = d
	} else if prior.Version == d.Version && d.Text != "" {
		prior.Text = d.Text
	}
	prior.NeedExclusionText = exclusion
	union[d.Ref.Key()] = prior
}
