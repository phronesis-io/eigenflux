// Package discoveryserve owns the typed serving envelope and atomic Redis
// history/sample/idempotency commit. Ranking remains in Sort.
package discoveryserve

import (
	"context"
	"crypto/sha256"
	"eigenflux_server/pkg/bloomfilter"
	"eigenflux_server/pkg/discovery"
	"eigenflux_server/pkg/impr"
	"eigenflux_server/pkg/replaylog"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/redis/go-redis/v9"
	"strconv"
	"time"
)

type Executor interface {
	Execute(context.Context, int64, discovery.Request, discovery.Mode, int64) (discovery.Execution, error)
	Revalidate(context.Context, int64, discovery.Execution, int64) error
}
type Service struct {
	Redis        *redis.Client
	IDs          discovery.IDGenerator
	Executor     Executor
	StreamMaxLen int64
	DisableDedup bool
}
type cached struct {
	Hash      string              `json:"hash"`
	Execution discovery.Execution `json:"execution"`
	Response  discovery.Response  `json:"response"`
}
type claimWrite struct {
	Key string `json:"key"`
	TTL int64  `json:"ttl"`
}
type setWrite struct {
	Key    string `json:"key"`
	Member string `json:"member"`
}

const commitScript = `
if redis.call('GET',KEYS[1])~=ARGV[1] then return 0 end
if KEYS[5]~='' and redis.call('GET',KEYS[5])~=ARGV[12] then return 0 end
local writes=cjson.decode(ARGV[4])
for _,w in ipairs(writes) do local t=redis.call('TYPE',w.key).ok;if t~='none' and t~='set' then return redis.error_reply('history type mismatch') end end
local t=redis.call('TYPE',KEYS[3]).ok;if t~='none' and t~='stream' then return redis.error_reply('stream type mismatch') end
for _,w in ipairs(writes) do redis.call('SADD',w.key,w.member);redis.call('EXPIRE',w.key,2592000) end
for _,c in ipairs(cjson.decode(ARGV[9])) do redis.call('SET',c.key,'1','EX',c.ttl,'NX') end
if ARGV[5]~='[]' then
local args={KEYS[3]};if tonumber(ARGV[10])>0 then table.insert(args,'MAXLEN');table.insert(args,'~');table.insert(args,ARGV[10]) end
table.insert(args,'*');for _,v in ipairs({'impression_id',ARGV[1],'agent_id',ARGV[6],'agent_features',ARGV[7],'served_at',ARGV[8],'items',ARGV[5],'delivered','1'}) do table.insert(args,v) end
redis.call('XADD',unpack(args)) end

if KEYS[4]~='' then redis.call('SET',KEYS[4],ARGV[11],'EX',1800) end
redis.call('SET',KEYS[2],ARGV[2],'EX',86400)
redis.call('DEL',KEYS[1])
return 1`

func (s Service) Serve(ctx context.Context, owner int64, r discovery.Request, mode discovery.Mode, key string) (discovery.Response, error) {
	return s.serve(ctx, owner, r, mode, key, commitPage{})
}

type commitPage struct {
	Impression              string
	Position                int
	Key, Value, Lock, Token string
}

func (s Service) serve(ctx context.Context, owner int64, r discovery.Request, mode discovery.Mode, key string, page commitPage) (discovery.Response, error) {
	empty := discovery.Response{}
	if owner <= 0 {
		return empty, discovery.Failure(401, "unauthorized")
	}
	if len(key) > 128 {
		return empty, discovery.Invalid("idempotency_key", "too_long")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	body, err := json.Marshal(struct {
		Mode    discovery.Mode
		Request discovery.Request
	}{mode, r})
	if err != nil {
		return empty, err
	}
	digest := sha256.Sum256(body)
	hash := hex.EncodeToString(digest[:])
	id, err := s.IDs.NextID()
	if err != nil {
		return empty, err
	}
	impression := strconv.FormatInt(id, 10)
	if page.Impression != "" {
		impression = page.Impression
	}
	if key == "" {
		key = "request:" + impression
	}
	kh := sha256.Sum256([]byte(key))
	cacheKey := fmt.Sprintf("discovery:serve:%d:%x", owner, kh)
	lockKey := cacheKey + ":lock"
	load := func() (discovery.Response, bool, error) {
		raw, e := s.Redis.Get(ctx, cacheKey).Result()
		if e == redis.Nil {
			return empty, false, nil
		}
		if e != nil {
			return empty, false, e
		}
		var prior cached
		if e = json.Unmarshal([]byte(raw), &prior); e != nil {
			return empty, false, e
		}
		if prior.Hash != hash {
			return empty, true, discovery.Failure(409, "idempotency_conflict")
		}
		if e = s.Executor.Revalidate(ctx, owner, prior.Execution, time.Now().UnixMilli()); e != nil {
			return empty, true, e
		}
		return prior.Response, true, nil
	}
	if response, ok, err := load(); err != nil || ok {
		return response, err
	}
	locked, err := s.Redis.SetNX(ctx, lockKey, impression, 60*time.Second).Result()
	if err != nil {
		return empty, err
	}
	if !locked {
		return empty, discovery.Failure(409, "request_in_progress")
	}
	defer func() {
		cleanup, done := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer done()
		s.Redis.Eval(cleanup, `if redis.call('GET',KEYS[1])==ARGV[1] then return redis.call('DEL',KEYS[1]) end return 0`, []string{lockKey}, impression)
	}()
	if response, ok, err := load(); err != nil || ok {
		return response, err
	}
	executeMode := mode
	if mode == "legacy_search" {
		mode = discovery.Search
	}
	now := time.Now().UnixMilli()
	x, err := s.Executor.Execute(ctx, owner, r, executeMode, now)
	if err != nil {
		return empty, err
	}
	if err = s.Executor.Revalidate(ctx, owner, x, time.Now().UnixMilli()); err != nil {
		return empty, err
	}
	response := discovery.Response{RequestID: impression, ImpressionID: impression, Mode: mode, PipelineVersion: discovery.PipelineVersion, Items: []discovery.ResultItem{}, Status: x.Status, Partial: len(x.PartialReasons) > 0, Reasons: x.PartialReasons, FallbackReason: x.FallbackReason, ConstraintMode: "explicit_filters"}
	if len(x.Contexts) > 0 {
		c := x.Contexts[0]
		if len(x.Candidates) > 0 {
			c = x.Candidates[0].Context
		}
		if c.Need != nil {
			response.ConstraintMode = "structured_need"
		}
		response.Origin = c.Origin
		response.ContextID = c.ID
		response.EffectiveFilters = c.Filters
	}
	writes := []setWrite{}
	claims := []claimWrite{}
	items := []replaylog.ServedItem{}
	for pos, c := range x.Candidates {
		response.Items = append(response.Items, discovery.PublicItem(c))
		features, e := json.Marshal(map[string]any{"search": c})
		if e != nil {
			return empty, e
		}
		si := replaylog.ServedItem{PipelineVersion: "need_search_v1", RequestMode: string(mode), SampleSchemaVersion: 2, SourceKind: string(c.Document.Ref.Type), SourceID: c.Document.Ref.ID, ContextID: c.Context.ID, ItemFeatures: string(features), Score: c.FinalScore, Position: pos + page.Position}
		if c.Context.NeedID() != 0 {
			si.NeedID = c.Context.NeedID()
			si.NeedRevision = c.Context.Revision
			if c.Context.SourceNeedID > 0 {
				si.NeedRevision = c.Context.SourceNeedRevision
			}
		}
		d := c.Document
		if mode == discovery.Recommendation && d.Ref.Type == discovery.Broadcast && c.ClaimTTLSeconds > 0 {
			claims = append(claims, claimWrite{fmt.Sprintf("sort:inject:claim:%d", d.Ref.ID), c.ClaimTTLSeconds})
		}
		if d.Ref.Type == discovery.Broadcast {
			si.ItemID = d.Ref.ID
			itemKey := fmt.Sprintf(impr.KeyItemIDs, owner)
			if mode == discovery.Search {
				itemKey = fmt.Sprintf("impr:search:agent:%d:items", owner)
			}
			writes = append(writes, setWrite{itemKey, fmt.Sprint(d.Ref.ID)})
			if mode == discovery.Recommendation {
				if d.GroupID > 0 {
					writes = append(writes, setWrite{fmt.Sprintf(impr.KeyGroupIDs, owner), fmt.Sprint(d.GroupID)})
					if !s.DisableDedup {
						writes = append(writes, setWrite{bloomfilter.GetKeyForDate(time.Now()), fmt.Sprintf("%d:%d", owner, d.GroupID)})
					}
				}
				if d.URL != "" {
					writes = append(writes, setWrite{fmt.Sprintf(impr.KeyURLs, owner), d.URL})
				}
			}
		} else if mode == discovery.Recommendation {
			writes = append(writes, setWrite{fmt.Sprintf("impr:discovery:agent:%d:items", owner), d.Ref.Key()})
		}
		if e = si.Validate(); e != nil {
			return empty, e
		}
		items = append(items, si)
	}
	frozen, err := json.Marshal(cached{hash, x, response})
	if err != nil {
		return empty, err
	}
	ws, err := json.Marshal(writes)
	if err != nil {
		return empty, err
	}
	sample, err := json.Marshal(items)
	if err != nil {
		return empty, err
	}
	contexts, err := json.Marshal(map[string]any{"pipeline_version": "need_search_v1", "search_context": map[string]any{"contexts": x.Contexts, "request_mode": mode, "request_time": now, "fallback_reason": x.FallbackReason}})
	if err != nil {
		return empty, err
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return empty, err
	}
	result, err := s.Redis.Eval(ctx, commitScript, []string{lockKey, cacheKey, replaylog.StreamName, page.Key, page.Lock}, impression, string(frozen), hash, string(ws), string(sample), fmt.Sprint(owner), string(contexts), fmt.Sprint(time.Now().UnixMilli()), string(claimsJSON), s.StreamMaxLen, page.Value, page.Token).Int()
	if err != nil {
		return empty, err
	}
	if result != 1 {
		return empty, discovery.Failure(503, "serving_commit_lost")
	}
	return response, nil
}
