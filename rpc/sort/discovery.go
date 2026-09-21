package main

import (
	"context"
	"crypto/sha256"
	"eigenflux_server/kitex_gen/eigenflux/commission/commissionservice"
	"eigenflux_server/kitex_gen/eigenflux/order/orderservice"
	sortapi "eigenflux_server/kitex_gen/eigenflux/sort"
	"eigenflux_server/pipeline/embedding"
	"eigenflux_server/pkg/agentindex"
	"eigenflux_server/pkg/commissionsource"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/discovery"
	"eigenflux_server/pkg/discoveryrpc"
	"eigenflux_server/pkg/es"
	"eigenflux_server/pkg/idgen"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/pkg/rpcx"
	"eigenflux_server/pkg/taxonomy"
	"eigenflux_server/rpc/sort/discoverydb"
	"eigenflux_server/rpc/sort/discoverysource"
	"encoding/json"
	"fmt"
	etcd "github.com/kitex-contrib/registry-etcd"
	"os"
	"strings"
	"time"
)

var discoveryEngine *discovery.Engine
var discoveryStore discoverydb.Store

func initDiscovery(ctx context.Context) (func(), error) {
	if !cfg.EnableNeedSearch {
		return func() {}, nil
	}
	if err := cfg.ValidateCommissionDiscoveryConfiguration(); err != nil {
		return nil, err
	}
	v, err := taxonomy.Configure(cfg.DiscoveryTaxonomyPath)
	if err != nil {
		return nil, err
	}
	if err := v.ValidateEmbedding(cfg.EmbeddingModel, cfg.EmbeddingDimensions); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(cfg.DiscoveryRulesPath)
	if err != nil {
		return nil, err
	}
	rules, err := discovery.Decode[discovery.Rules](raw)
	if err != nil {
		return nil, err
	}
	if err = rules.Validate(); err != nil {
		return nil, err
	}
	embed := embedding.NewClient(cfg.EmbeddingProvider, cfg.EmbeddingApiKey, cfg.EmbeddingBaseURL, cfg.EmbeddingModel, cfg.EmbeddingDimensions)
	if err = agentindex.Ensure(ctx, cfg.AgentDiscoveryIndex, cfg.EmbeddingDimensions); err != nil {
		return nil, err
	}
	ids, err := idgen.NewManagedGenerator(ctx, idgen.ManagedGeneratorConfig{Endpoints: strings.Split(cfg.EtcdAddr, ","), WorkerPrefix: cfg.IDWorkerPrefix, ServiceName: "discovery-context-id", InstanceID: cfg.IDInstanceID, LeaseTTLSecond: cfg.IDWorkerLeaseTTL, EpochMS: cfg.IDSnowflakeEpoch})
	if err != nil {
		return nil, err
	}
	close := func() { _ = ids.Close(context.Background()) }
	resolver, err := etcd.NewEtcdResolver(strings.Split(cfg.EtcdAddr, ","))
	if err != nil {
		close()
		return nil, err
	}
	cc, err := commissionservice.NewClient(cfg.CommissionSourceService, rpcx.ClientOptions(resolver)...)
	if err != nil {
		close()
		return nil, err
	}
	oc, err := orderservice.NewClient(cfg.OrderSourceService, rpcx.ClientOptions(resolver)...)
	if err != nil {
		close()
		return nil, err
	}
	index := cfg.CommissionIndexAlias
	if index == "" {
		index = cfg.CommissionIndexName
	}
	if err := es.EnsureRetrievalSlots(ctx, es.ReadIndexPattern, index, cfg.CommissionIndexName); err != nil {
		close()
		return nil, err
	}
	discoveryStore = discoverydb.Store{DB: db.DB}
	discoveryEngine = &discovery.Engine{Compiler: &discovery.Compiler{Taxonomy: v, Embedder: embed, EmbeddingVersion: cfg.EmbeddingModel}, Store: discoveryStore, IDs: ids, Rules: rules, Policies: discoveryPolicies, Sources: &discoverysource.Source{DB: db.DB, Redis: mq.RDB, Commission: commissionsource.Adapter{Commission: cc, Order: oc}, CommissionIndex: index, AgentIndex: cfg.AgentDiscoveryIndex, RecallNamespace: cfg.RecallRedisNamespace, BlockedAuthorEmails: cfg.BlockedAgentEmails, DisableDedup: cfg.ShouldDisableDedup(), DisabledChannels: map[string]bool{"hot_recall": !cfg.EnableHotRecall, "new_recall": !cfg.EnableNewRecall, "new_ugc_recall": !cfg.EnableNewUGCRecall}}}
	return close, nil
}
func (s *SortServiceESImpl) Discovery(ctx context.Context, req *sortapi.DiscoveryReq) (*sortapi.DiscoveryResp, error) {
	if req == nil || req.AgentId <= 0 {
		return discoveryrpc.Response(nil, discovery.Failure(401, "unauthorized")), nil
	}
	if discoveryEngine == nil {
		return discoveryrpc.Response(nil, discovery.Failure(503, "discovery_disabled")), nil
	}
	value, err := discoveryOperation(ctx, req, time.Now().UnixMilli())
	return discoveryrpc.Response(value, err), nil
}
func discoveryOperation(ctx context.Context, r *sortapi.DiscoveryReq, now int64) (any, error) {
	e := discoveryEngine
	switch r.Operation {
	case "search", "recommendation", "legacy_search", "legacy_prefetch":
		in, err := discovery.Decode[discovery.Request]([]byte(r.Payload))
		if err != nil {
			return nil, err
		}
		mode := discovery.Mode(r.Operation)
		if r.Operation == "legacy_search" {
			in.LegacyLimit = true
			mode = discovery.Search
		}
		if r.Operation == "legacy_prefetch" {
			in.LegacyPrefetch = 20
			mode = discovery.Recommendation
		}
		return e.Execute(ctx, r.AgentId, in, mode, now)
	case "refresh_page":
		var x discovery.Execution
		if err := json.Unmarshal([]byte(r.Payload), &x); err != nil {
			return nil, err
		}
		return e.RefreshPage(ctx, r.AgentId, x, now)
	case "revalidate":
		var x discovery.Execution
		if err := json.Unmarshal([]byte(r.Payload), &x); err != nil {
			return nil, err
		}
		return x, e.Revalidate(ctx, r.AgentId, x, now)
	case "need_create", "need_update":
		input, err := discovery.Decode[discovery.NeedInput]([]byte(r.Payload))
		if err != nil {
			return nil, err
		}
		raw, _ := json.Marshal(input)
		bodyHash := fmt.Sprintf("%x", sha256.Sum256(raw))
		if r.Operation == "need_create" {
			prior, found, err := discoveryStore.Idempotent(ctx, r.AgentId, r.GetIdempotencyKey(), bodyHash)
			if err != nil || found {
				return prior, err
			}
		}
		id := r.GetResourceId()
		if r.Operation == "need_create" {
			if input.ExpectedRevision != 0 {
				return nil, discovery.Invalid("expected_revision", "create_must_omit")
			}
			id, err = e.IDs.NextID()
			if err != nil {
				return nil, err
			}
		} else {
			if _, err = discoveryStore.Get(ctx, r.AgentId, id, true); err != nil {
				return nil, err
			}
		}
		languages := []string{}
		if input.Defaults.Language == "card" {
			owner, err := e.Sources.Owner(ctx, r.AgentId)
			if err != nil {
				return nil, err
			}
			languages = owner.Languages
		}
		compiled, err := e.Compiler.Need(ctx, r.AgentId, id, now, input, languages, true)
		if err != nil {
			return nil, err
		}
		if r.Operation == "need_create" {
			return discoveryStore.CreateWithHash(ctx, compiled, r.GetIdempotencyKey(), bodyHash)
		}
		return discoveryStore.Update(ctx, compiled, input.ExpectedRevision)
	case "need_get":
		c, err := discoveryStore.Get(ctx, r.AgentId, r.GetResourceId(), true)
		if err == nil && c.State == "active" && !c.Active(now) {
			c.State = "expired"
		}
		return c, err
	case "need_list":
		in, err := discovery.Decode[struct {
			State  string `json:"state"`
			Cursor int64  `json:"cursor,string"`
			Limit  int    `json:"limit"`
		}]([]byte(r.Payload))
		if err != nil {
			return nil, err
		}
		if in.Limit == 0 {
			in.Limit = 20
		}
		if in.Limit < 1 || in.Limit > 100 || in.Cursor < 0 {
			return nil, discovery.Invalid("pagination", "invalid")
		}
		rows, err := discoveryStore.List(ctx, r.AgentId, in.State, in.Cursor, in.Limit+1, now)
		if err != nil {
			return nil, err
		}
		cursor := ""
		if len(rows) > in.Limit {
			rows = rows[:in.Limit]
			cursor = fmt.Sprint(rows[len(rows)-1].ID)
		}
		return map[string]any{"needs": rows, "next_cursor": cursor}, nil
	case "need_state":
		in, err := discovery.Decode[struct {
			State    string `json:"state"`
			Revision int64  `json:"expected_revision"`
		}]([]byte(r.Payload))
		if err != nil {
			return nil, err
		}
		return discoveryStore.SetState(ctx, r.AgentId, r.GetResourceId(), in.Revision, in.State, now)
	case "taxonomy":
		in, err := discovery.Decode[struct {
			Query    string `json:"query"`
			Category string `json:"category"`
			Subtype  string `json:"subtype"`
			Limit    int    `json:"limit"`
		}]([]byte(r.Payload))
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(in.Query) == "" || len(in.Query) > 8000 {
			return nil, discovery.Invalid("query", "invalid_length")
		}
		if in.Limit == 0 {
			in.Limit = 10
		}
		if in.Limit < 1 || in.Limit > 20 {
			return nil, discovery.Invalid("limit", "out_of_range")
		}
		if in.Category != "" && !e.Compiler.Taxonomy.ValidBranch(in.Category, in.Subtype) {
			return nil, discovery.Invalid("category", "invalid_parent")
		}
		v, err := e.Compiler.Embedder.GetEmbedding(ctx, in.Query)
		if err != nil {
			return nil, discovery.Failure(503, "embedding_unavailable")
		}
		return map[string]any{"matches": e.Compiler.Taxonomy.Search(in.Query, in.Category, in.Subtype, v, .8, in.Limit), "taxonomy_version": e.Compiler.Taxonomy.Version}, nil
	}
	return nil, discovery.Invalid("operation", "unsupported")
}
