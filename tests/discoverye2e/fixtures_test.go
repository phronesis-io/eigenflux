package discoverye2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"eigenflux_server/kitex_gen/eigenflux/base"
	"eigenflux_server/kitex_gen/eigenflux/commission"
	"eigenflux_server/kitex_gen/eigenflux/commission/commissionservice"
	"eigenflux_server/kitex_gen/eigenflux/order"
	"eigenflux_server/kitex_gen/eigenflux/order/orderservice"
	"eigenflux_server/pkg/agentidentity"
	"eigenflux_server/pkg/agentindex"
	"eigenflux_server/pkg/commissionindex"
	"eigenflux_server/pkg/es"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/pkg/rpcx"
	sortdal "eigenflux_server/rpc/sort/dal"

	"github.com/cloudwego/kitex/server"
	etcd "github.com/kitex-contrib/registry-etcd"
	"github.com/stretchr/testify/require"
)

func (s *stack) sql(t *testing.T, q string, args ...any) {
	t.Helper()
	require.NoError(t, s.db.Exec(q, args...).Error)
}

func (s *stack) seed(t *testing.T) {
	t.Helper()
	owners := []int64{s.owner, s.other, s.author}
	t.Cleanup(func() {
		// Workers and application processes stop before fixture rows are removed.
		for _, table := range []string{"replay_logs", "discovery_contexts"} {
			s.sql(t, "DELETE FROM "+table+" WHERE agent_id IN ?", owners)
		}
		s.sql(t, "DELETE FROM user_relations WHERE from_uid IN ? OR to_uid IN ?", owners, owners)
		s.sql(t, "DELETE FROM processed_items WHERE item_id=?", s.item)
		s.sql(t, "DELETE FROM raw_items WHERE item_id=?", s.item)
		s.sql(t, "DELETE FROM agents WHERE agent_id IN ?", owners)
		resp, err := es.Client.DeleteByQuery([]string{es.ReadIndexPattern}, bytes.NewBufferString(fmt.Sprintf(`{"query":{"term":{"id":%d}}}`, s.item)), es.Client.DeleteByQuery.WithRefresh(true))
		require.NoError(t, err)
		_ = resp.Body.Close()
		require.False(t, resp.IsError())
		resp, err = es.Client.Indices.Delete([]string{s.commissionIndex, s.agentIndex}, es.Client.Indices.Delete.WithIgnoreUnavailable(true))
		require.NoError(t, err)
		_ = resp.Body.Close()
		require.False(t, resp.IsError())
		for _, id := range append(append([]int64{}, owners...), s.item) {
			keys, err := mq.RDB.Keys(context.Background(), fmt.Sprintf("*:%d:*", id)).Result()
			require.NoError(t, err)
			if len(keys) > 0 {
				require.NoError(t, mq.RDB.Del(context.Background(), keys...).Err())
			}
		}
	})
	now := time.Now().UnixMilli()
	s.shortIDs = map[int64]string{}
	for _, id := range owners {
		shortID, err := agentidentity.GenerateShortID()
		require.NoError(t, err)
		s.shortIDs[id] = shortID
		s.sql(t, "INSERT INTO agents(agent_id,short_id,email,agent_name,agent_name_en,created_at,updated_at,profile_completed_at) VALUES(?,?,?,?,?,?,?,?)", id, shortID, fmt.Sprintf("discovery-e2e-%d@example.invalid", id), fmt.Sprintf("精确查找-%d", id), fmt.Sprintf("Exact designer %d", id), now, now, now)
		s.sql(t, "INSERT INTO agent_context_revisions(agent_id,revision,compiled_context,generated_at) VALUES(?,1,?::jsonb,?)", id, `{"intents":[{"watch_for":"landing page design","trigger_when":"design request"}]}`, now)
		s.sql(t, "INSERT INTO agent_onboarding_v2(agent_id,state,current_step,active_context_revision,completed_at,created_at,updated_at) VALUES(?,'completed',5,1,?,?,?)", id, now, now, now)
		card, _ := json.Marshal(map[string]any{"display_name": "E2E designer", "agent_description": "landing page design", "working_languages": []string{"en"}, "offering": []string{s.category, "landing-page"}, "last_active_at": now})
		s.sql(t, "INSERT INTO agent_cards(agent_id,public_card,private_card,schema_version,source_version,card_version,generated_at,rebuild_fence,public_card_version,public_card_generated_at) VALUES(?,?::jsonb,?::jsonb,1,1,1,?,1,1,?)", id, string(card), `{"secret":"private-e2e-marker"}`, now, now)
	}
	s.token = s.seedSession(t, s.owner, "{feed:read,context:read,context:write}")
	s.otherToken = s.seedSession(t, s.other, "{feed:read,context:read,context:write}")
	s.sql(t, "INSERT INTO raw_items(item_id,author_agent_id,raw_content,created_at) VALUES(?,?,?,?)", s.item, s.author, "landing page design", now)
	s.sql(t, "INSERT INTO processed_items(item_id,status,summary,broadcast_type,source_type,quality_score,lang,domains,keywords,group_id,updated_at) VALUES(?,3,'landing page design','info','original',0.8,'en',?,'landing-page',?,?)", s.item, s.category, s.item, now)
	require.NoError(t, sortdal.IndexItem(context.Background(), &sortdal.Item{ID: s.item, AuthorAgentID: s.author, Content: "landing page design", Summary: "landing page design", Type: "info", SourceType: "original", Lang: "en", Domains: []string{s.category}, Keywords: []string{"landing-page"}, QualityScore: .8, GroupID: s.item, CreatedAt: time.UnixMilli(now), UpdatedAt: time.UnixMilli(now), Embedding: s.vector}))
	resp, err := es.Client.Indices.Refresh(es.Client.Indices.Refresh.WithIndex(es.IndexName))
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.False(t, resp.IsError())
	cat := commissionindex.CatalogueSnapshot{CommissionID: s.item, SellerAgentID: s.author, Status: "active", CatalogueVersion: 1, Title: "landing page design", Tags: []string{s.category, "landing-page"}, Currency: "CNY", PriceFen: 0, PromisedDeliveryMS: 1000, CreatedAt: now, UpdatedAt: now}
	s.catalogue = &catalogueFixture{snapshot: cat}
	b, _ := json.Marshal(map[string]any{"mappings": commissionindex.Mapping(len(s.vector))})
	resp, err = es.Client.Indices.Create(s.commissionIndex, es.Client.Indices.Create.WithBody(bytes.NewReader(b)))
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.False(t, resp.IsError())
	doc := commissionindex.BuildDocument(cat, commissionindex.StatisticsSnapshot{}, s.vector)
	require.NoError(t, (commissionindex.ESStore{Redis: mq.RDB, Index: s.commissionIndex}).Upsert(context.Background(), doc))
	response, err := es.Client.Indices.Refresh(es.Client.Indices.Refresh.WithIndex(s.commissionIndex))
	require.NoError(t, err)
	_ = response.Body.Close()
	require.False(t, response.IsError())
	require.NoError(t, agentindex.Ensure(context.Background(), s.agentIndex, len(s.vector)))
	docs, err := agentindex.Load(context.Background(), s.db, []int64{s.author})
	require.NoError(t, err)
	require.Len(t, docs, 1)
	docs[0].Embedding = s.vector
	require.NoError(t, agentindex.WriteForward(context.Background(), mq.RDB, s.agentIndex, docs[0]))
	s.index(t, s.agentIndex, s.author, docs[0].SearchFields())
}

func (s *stack) seedSession(t *testing.T, owner int64, scopes string) string {
	t.Helper()
	now := time.Now().UnixMilli()
	token := fmt.Sprintf("efv2a_e2e_%d_%d", owner, time.Now().UnixNano())
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(token)))
	var principal int64
	err := s.db.Raw("INSERT INTO agent_principals(agent_id,key_fingerprint,public_key,status,created_at,last_seen_at) VALUES(?,?,?,'active',?,?) RETURNING principal_id", owner, hash, make([]byte, 32), now, now).Scan(&principal).Error
	require.NoError(t, err)
	s.sql(t, "INSERT INTO agent_credential_sessions(principal_id,family_id,access_token_hash,refresh_token_hash,audience,scopes,issued_at,expires_at,absolute_expires_at,last_seen_at) VALUES(?,?,?,?,'agent_v2',?::text[],?,?,?,?)", principal, hash, hash, hash+"refresh", scopes, now, now+3600000, now+3600000, now)
	return token
}

func (s *stack) index(t *testing.T, index string, id int64, doc any) {
	t.Helper()
	b, err := json.Marshal(doc)
	require.NoError(t, err)
	r, err := es.Client.Index(index, bytes.NewReader(b), es.Client.Index.WithDocumentID(strconv.FormatInt(id, 10)), es.Client.Index.WithRefresh("true"))
	require.NoError(t, err)
	defer r.Body.Close()
	require.False(t, r.IsError(), "index %s: %s", index, r.String())
}

// External catalogue boundaries use real Kitex transport with deterministic data.
type catalogueFixture struct {
	mu       sync.RWMutex
	snapshot commissionindex.CatalogueSnapshot
	fail     bool
}

func (f *catalogueFixture) GetIndexSnapshot(_ context.Context, r *commission.GetIndexSnapshotReq) (*commission.GetIndexSnapshotResp, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	c := f.snapshot
	if f.fail || r.CommissionId != c.CommissionID {
		return &commission.GetIndexSnapshotResp{BaseResp: &base.BaseResp{Code: 503, Msg: "fixture unavailable"}}, nil
	}
	return &commission.GetIndexSnapshotResp{BaseResp: &base.BaseResp{}, Snapshot: &commission.CommissionIndexSnapshot{
		Definition: &commission.CommissionDefinition{CommissionId: c.CommissionID, SellerAgentId: c.SellerAgentID, Status: c.Status, PublicRevision: 1, Version: c.CatalogueVersion, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt},
		Revision:   &commission.CommissionRevision{CommissionId: c.CommissionID, SellerAgentId: c.SellerAgentID, Revision: 1, Content: &commission.CommissionInput{Title: c.Title, Tags: c.Tags, PriceFen: c.PriceFen, Currency: c.Currency, PromisedDeliveryMs: c.PromisedDeliveryMS}},
	}}, nil
}
func (f *catalogueFixture) ListActiveIndexSnapshots(context.Context, *commission.ListActiveIndexSnapshotsReq) (*commission.ListActiveIndexSnapshotsResp, error) {
	return &commission.ListActiveIndexSnapshotsResp{BaseResp: &base.BaseResp{}}, nil
}
func (f *catalogueFixture) GetCommissionStatistics(_ context.Context, r *order.GetCommissionStatisticsReq) (*order.GetCommissionStatisticsResp, error) {
	return &order.GetCommissionStatisticsResp{Statistics: &order.CommissionStatistics{CommissionId: r.CommissionId}, BaseResp: &base.BaseResp{}}, nil
}
func (f *catalogueFixture) BatchGetCommissionStatistics(_ context.Context, r *order.BatchGetCommissionStatisticsReq) (*order.BatchGetCommissionStatisticsResp, error) {
	out := &order.BatchGetCommissionStatisticsResp{BaseResp: &base.BaseResp{}}
	for _, id := range r.CommissionIds {
		out.Statistics = append(out.Statistics, &order.CommissionStatistics{CommissionId: id, StatisticsVersion: 1})
	}
	return out, nil
}
func (s *stack) startCatalogue(t *testing.T) {
	t.Helper()
	for _, name := range []string{"DiscoveryE2ECommission", "DiscoveryE2EOrder"} {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		r, err := etcd.NewEtcdRegistry(strings.Split(s.cfg.EtcdAddr, ","))
		require.NoError(t, err)
		opts := rpcx.ServerOptions(l.Addr(), r, name, server.WithListener(l))
		var srv server.Server
		if name == "DiscoveryE2ECommission" {
			srv = commissionservice.NewServer(s.catalogue, opts...)
		} else {
			srv = orderservice.NewServer(s.catalogue, opts...)
		}
		done := make(chan error, 1)
		go func() { done <- srv.Run() }()
		t.Cleanup(func() {
			require.NoError(t, srv.Stop())
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(5 * time.Second):
				t.Error("fixture RPC did not stop")
			}
		})
	}
}
