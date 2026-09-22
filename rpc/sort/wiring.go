package main

import (
	"context"
	"eigenflux_server/kitex_gen/eigenflux/commission/commissionservice"
	"eigenflux_server/kitex_gen/eigenflux/order/orderservice"
	"eigenflux_server/pipeline/embedding"
	"eigenflux_server/pkg/agentindex"
	"eigenflux_server/pkg/commissionsource"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/es"
	"eigenflux_server/pkg/idgen"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/pkg/rpcx"
	"eigenflux_server/rpc/sort/discovery"
	searchindex "eigenflux_server/rpc/sort/discovery/index"

	"os"
	"strings"

	etcd "github.com/kitex-contrib/registry-etcd"
)

func initDiscovery(ctx context.Context, cfg *config.Config, policies func(context.Context, []discovery.Candidate, discovery.Mode, int) ([]discovery.Candidate, error)) (*discovery.Service, func(), error) {
	if !cfg.EnableNeedSearch {
		return nil, func() {}, nil
	}
	if err := cfg.ValidateCommissionDiscoveryConfiguration(); err != nil {
		return nil, nil, err
	}
	v, err := searchindex.Configure(cfg.DiscoveryTaxonomyPath)
	if err != nil {
		return nil, nil, err
	}
	if err := v.ValidateEmbedding(cfg.EmbeddingModel, cfg.EmbeddingDimensions); err != nil {
		return nil, nil, err
	}
	raw, err := os.ReadFile(cfg.DiscoveryRulesPath)
	if err != nil {
		return nil, nil, err
	}
	rules, err := discovery.Decode[discovery.Rules](raw)
	if err != nil {
		return nil, nil, err
	}
	if err = rules.Validate(); err != nil {
		return nil, nil, err
	}
	embed := embedding.NewClient(cfg.EmbeddingProvider, cfg.EmbeddingApiKey, cfg.EmbeddingBaseURL, cfg.EmbeddingModel, cfg.EmbeddingDimensions)
	if err = agentindex.Ensure(ctx, cfg.AgentDiscoveryIndex, cfg.EmbeddingDimensions); err != nil {
		return nil, nil, err
	}
	ids, err := idgen.NewManagedGenerator(ctx, idgen.ManagedGeneratorConfig{Endpoints: strings.Split(cfg.EtcdAddr, ","), WorkerPrefix: cfg.IDWorkerPrefix, ServiceName: "discovery-context-id", InstanceID: cfg.IDInstanceID, LeaseTTLSecond: cfg.IDWorkerLeaseTTL, EpochMS: cfg.IDSnowflakeEpoch})
	if err != nil {
		return nil, nil, err
	}
	close := func() { _ = ids.Close(context.Background()) }
	resolver, err := etcd.NewEtcdResolver(strings.Split(cfg.EtcdAddr, ","))
	if err != nil {
		close()
		return nil, nil, err
	}
	cc, err := commissionservice.NewClient(cfg.CommissionSourceService, rpcx.ClientOptions(resolver)...)
	if err != nil {
		close()
		return nil, nil, err
	}
	oc, err := orderservice.NewClient(cfg.OrderSourceService, rpcx.ClientOptions(resolver)...)
	if err != nil {
		close()
		return nil, nil, err
	}
	index := cfg.CommissionIndexAlias
	if index == "" {
		index = cfg.CommissionIndexName
	}
	if err := es.EnsureRetrievalSlots(ctx, es.ReadIndexPattern, index, cfg.CommissionIndexName); err != nil {
		close()
		return nil, nil, err
	}
	store := discovery.Store{DB: db.DB}
	engine := &discovery.Engine{Compiler: &discovery.Compiler{Taxonomy: v, Embedder: embed, EmbeddingVersion: cfg.EmbeddingModel}, Store: store, IDs: ids, Rules: rules, Policies: policies, Sources: &discovery.Source{DB: db.DB, Redis: mq.RDB, Commission: commissionsource.Adapter{Commission: cc, Order: oc}, CommissionIndex: index, AgentIndex: cfg.AgentDiscoveryIndex, RecallNamespace: cfg.RecallRedisNamespace, BlockedAuthorEmails: cfg.BlockedAgentEmails, DisableDedup: cfg.ShouldDisableDedup(), DisabledChannels: map[string]bool{"hot_recall": !cfg.EnableHotRecall, "new_recall": !cfg.EnableNewRecall, "new_ugc_recall": !cfg.EnableNewUGCRecall}}}
	return &discovery.Service{Engine: engine, Store: store}, close, nil
}
