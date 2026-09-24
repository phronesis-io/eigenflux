package discoverye2e

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"testing"
	"time"

	"eigenflux_server/pkg/impr"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/pkg/replaylog"
	"eigenflux_server/rpc/sort/discovery"

	"github.com/stretchr/testify/require"
)

func (s *stack) need(kind string) discovery.NeedInput {
	return discovery.NeedInput{NeedType: kind, Priority: .8, Target: discovery.Target{Category: s.category, FreeText: "landing page design", ProposedIntents: []string{"landing page"}}, Outcome: "design a landing page"}
}
func (s *stack) search(t *testing.T, r discovery.Request, key string) discovery.Response {
	t.Helper()
	raw := s.call(t, "POST", "/api/v2/discovery/search", s.token, key, r, 200)
	require.NotContains(t, string(raw), "private-e2e-marker")
	require.NotContains(t, string(raw), `"score":`)
	return decode[discovery.Response](t, raw)
}
func (s *stack) recommend(t *testing.T, r discovery.Request, key string) discovery.Response {
	t.Helper()
	return decode[discovery.Response](t, s.call(t, "POST", "/api/v2/discovery/recommendations", s.token, key, r, 200))
}
func (s *stack) saved(t *testing.T, kind string) discovery.Context {
	t.Helper()
	return decode[discovery.Context](t, s.call(t, "POST", "/api/v2/needs", s.token, "", s.need(kind), 200))
}
func (s *stack) waitSamples(t *testing.T, impression string, count int) {
	t.Helper()
	require.Eventually(t, func() bool {
		var n int64
		err := s.db.Table("replay_logs").Where("impression_id=? AND agent_id=?", impression, s.owner).Count(&n).Error
		return err == nil && n == int64(count)
	}, 10*time.Second, 50*time.Millisecond, "delivered samples must reach the existing replay table")
}

func TestDiscoveryE2E(t *testing.T) {
	s := startStack(t)
	ctx := context.Background()
	t.Run("NeedJSONNormalizationOwnershipAndLifecycle", func(t *testing.T) {
		defer s.sql(t, "DELETE FROM discovery_contexts WHERE agent_id=? AND persistence='saved'", s.owner)
		in := s.need("find_info")
		in.Defaults.Language = "card"
		s.call(t, "POST", "/api/v2/needs", "", "", in, 401)
		readOnly := s.seedSession(t, s.owner, "{context:read}")
		s.call(t, "POST", "/api/v2/needs", readOnly, "", in, 403)
		created := decode[discovery.Context](t, s.call(t, "POST", "/api/v2/needs", s.token, "need-create", in, 200))
		require.Positive(t, created.ID)
		require.Equal(t, s.owner, created.OwnerID)
		require.Equal(t, "saved", created.Persistence)
		require.EqualValues(t, 1, created.Revision)
		require.Equal(t, []string{"en"}, created.Filters.Lang)
		require.Equal(t, []string{"landing-page"}, created.SoftIntents)
		require.Equal(t, in.Target, created.Need.Target)
		require.Contains(t, created.Filters.ExcludeAuthors, strconv.FormatInt(s.owner, 10))
		var stored struct {
			Compiled  string
			Embedding []byte
		}
		require.NoError(t, s.db.Raw("SELECT compiled::text,embedding FROM discovery_contexts WHERE context_id=?", created.ID).Scan(&stored).Error)
		require.Equal(t, created.ID, decode[discovery.Context](t, []byte(stored.Compiled)).ID)
		require.Len(t, decode[[]float32](t, stored.Embedding), len(s.vector))
		again := decode[discovery.Context](t, s.call(t, "POST", "/api/v2/needs", s.token, "need-create", in, 200))
		require.Equal(t, created.ID, again.ID)
		changed := in
		changed.Outcome = "a different outcome"
		s.call(t, "POST", "/api/v2/needs", s.token, "need-create", changed, 409)
		path := fmt.Sprintf("/api/v2/needs/%d", created.ID)
		s.call(t, "GET", path, s.otherToken, "", nil, 404)
		changed.ExpectedRevision = 1
		s.call(t, "PUT", path, s.otherToken, "", changed, 404)
		s.call(t, "POST", path+"/state", s.otherToken, "", map[string]any{"state": "paused", "expected_revision": 1}, 404)
		s.call(t, "POST", "/api/v2/discovery/search", s.otherToken, "", discovery.Request{NeedID: created.ID}, 404)
		updated := decode[discovery.Context](t, s.call(t, "PUT", path, s.token, "", changed, 200))
		require.EqualValues(t, 2, updated.Revision)
		s.call(t, "PUT", path, s.token, "", changed, 409)
		paused := decode[discovery.Context](t, s.call(t, "POST", path+"/state", s.token, "", map[string]any{"state": "paused", "expected_revision": 2}, 200))
		require.EqualValues(t, 3, paused.Revision)
		s.call(t, "POST", "/api/v2/discovery/search", s.token, "", discovery.Request{NeedID: created.ID}, 409)
		s.call(t, "POST", path+"/state", s.token, "", map[string]any{"state": "active", "expected_revision": 3}, 200)
		s.call(t, "POST", path+"/state", s.token, "", map[string]any{"state": "completed", "expected_revision": 4}, 200)
		s.call(t, "POST", path+"/state", s.token, "", map[string]any{"state": "active", "expected_revision": 5}, 409)
		list := decode[struct {
			Needs []discovery.Context `json:"needs"`
		}](t, s.call(t, "GET", "/api/v2/needs?state=completed", s.token, "", nil, 200))
		require.Len(t, list.Needs, 1)
		require.Equal(t, created.ID, list.Needs[0].ID)
		s.call(t, "POST", "/api/v2/needs", s.token, "", map[string]any{"agent_id": fmt.Sprint(s.other)}, 400)
	})
	t.Run("ThreeKindSearchTypedSamplesAndIdempotency", func(t *testing.T) {
		r := discovery.Request{Query: "landing page design", Filters: discovery.Filters{Category: s.category}}
		first := s.search(t, r, "three-kinds")
		require.Equal(t, "need_search_v1", first.PipelineVersion)
		require.Equal(t, "ok", first.Status)
		require.Len(t, first.Items, 3)
		for i, kind := range []discovery.Kind{discovery.Broadcast, discovery.Commission, discovery.Agent} {
			require.Equal(t, kind, first.Items[i].Ref.Type)
			want := s.item
			if kind == discovery.Agent {
				want = s.author
			}
			require.Equal(t, want, first.Items[i].Ref.ID)
			require.Equal(t, "rules", first.Items[i].Match["scorer_type"])
		}
		s.waitSamples(t, first.ImpressionID, 3)
		var rows []struct {
			SourceKind                   string
			SourceID                     int64
			ItemID                       *int64
			PipelineVersion, RequestMode string
			SampleSchemaVersion          int
			ContextID                    int64
			ItemFeatures                 string
			Delivered                    bool
		}
		require.NoError(t, s.db.Table("replay_logs").Where("impression_id=?", first.ImpressionID).Find(&rows).Error)
		for _, row := range rows {
			require.Equal(t, "need_search_v1", row.PipelineVersion)
			require.Equal(t, "search", row.RequestMode)
			require.Equal(t, 2, row.SampleSchemaVersion)
			require.Positive(t, row.ContextID)
			require.True(t, row.Delivered)
			if row.SourceKind == "broadcast" {
				require.NotNil(t, row.ItemID)
				require.Equal(t, row.SourceID, *row.ItemID)
			} else {
				require.Nil(t, row.ItemID)
			}
			features := decode[map[string]json.RawMessage](t, []byte(row.ItemFeatures))
			require.Contains(t, features, "search")
		}
		again := s.search(t, r, "three-kinds")
		require.Equal(t, first, again)
		r.Limit = 2
		s.call(t, "POST", "/api/v2/discovery/search", s.token, "three-kinds", r, 409)
		require.Eventually(t, func() bool {
			return mq.RDB.SIsMember(ctx, fmt.Sprintf("impr:search:agent:%d:items", s.owner), fmt.Sprint(s.item)).Val()
		}, 3*time.Second, 25*time.Millisecond)
		require.False(t, mq.RDB.SIsMember(ctx, fmt.Sprintf(impr.KeyItemIDs, s.owner), fmt.Sprint(s.item)).Val(), "search must not consume automatic history")
		// A pre-upgrade event still lands in the same table with legacy markers.
		legacyID := fmt.Sprintf("e2e-legacy-%d", s.owner)
		require.NoError(t, replaylog.Publish(ctx, legacyID, s.owner, "{}", []replaylog.ServedItem{{ItemID: s.item, ItemFeatures: "{}", Score: .5}}))
		s.waitSamples(t, legacyID, 1)
		var old struct {
			PipelineVersion, RequestMode string
			SampleSchemaVersion          int
		}
		require.NoError(t, s.db.Table("replay_logs").Where("impression_id=?", legacyID).Take(&old).Error)
		require.Equal(t, "legacy_feed_v1", old.PipelineVersion)
		require.Equal(t, "feed", old.RequestMode)
		require.Equal(t, 1, old.SampleSchemaVersion)
	})
	t.Run("HardConstraintsAndInlineNeed", func(t *testing.T) {
		zero := int64(0)
		r := discovery.Request{Query: "landing page design", SourceKinds: []discovery.Kind{discovery.Commission}, Filters: discovery.Filters{BudgetMaxFen: &zero, Currency: "CNY"}}
		require.Len(t, s.search(t, r, "").Items, 1, "a known zero price must pass a zero budget")
		limit := int64(500)
		r.Filters.MaxDurationMS = &limit
		require.Empty(t, s.search(t, r, "").Items)
		r.Filters = discovery.Filters{ProviderRegion: []string{"CN"}}
		require.Empty(t, s.search(t, r, "").Items, "unknown provider region cannot satisfy a hard constraint")
		r.Filters = discovery.Filters{Category: s.category, Lang: []string{"zh"}}
		r.SourceKinds = []discovery.Kind{discovery.Broadcast}
		require.Empty(t, s.search(t, r, "").Items)
		r.Filters = discovery.Filters{BudgetMaxFen: &zero, Currency: "CNY"}
		s.call(t, "POST", "/api/v2/discovery/search", s.token, "", r, 400)
		need := s.need("find_people")
		result := s.search(t, discovery.Request{Need: &need}, "")
		require.Len(t, result.Items, 1)
		require.Equal(t, discovery.Agent, result.Items[0].Ref.Type)
		require.Equal(t, "inline_need", result.Origin)
		var savedCount int64
		require.NoError(t, s.db.Table("discovery_contexts").Where("agent_id=? AND persistence='saved'", s.owner).Count(&savedCount).Error)
		require.Zero(t, savedCount)
	})
	t.Run("KnownAgentRemainsSearchableButIsNotRecommended", func(t *testing.T) {
		need := s.saved(t, "find_people")
		defer s.sql(t, "DELETE FROM discovery_contexts WHERE context_id=?", need.ID)
		s.sql(t, "INSERT INTO user_relations(from_uid,to_uid,rel_type,created_at) VALUES(?,?,1,?)", s.owner, s.author, time.Now().UnixMilli())
		defer s.sql(t, "DELETE FROM user_relations WHERE from_uid=? AND to_uid=?", s.owner, s.author)
		r := discovery.Request{SourceKinds: []discovery.Kind{discovery.Agent}}
		// Omitted Need IDs must select the active saved Need automatically.
		recommended := s.recommend(t, r, "")
		require.Empty(t, recommended.Items)
		require.Equal(t, "no_match", recommended.Status)
		require.Equal(t, need.ID, recommended.ContextID)
		require.Empty(t, recommended.FallbackReason)
		r.Query = "landing page design"
		r.Filters.Category = s.category
		found := s.search(t, r, "")
		require.Len(t, found.Items, 1)
		require.Equal(t, s.author, found.Items[0].Ref.ID)
	})
	t.Run("RecommendationNeedDedupAndFrozenRetry", func(t *testing.T) {
		defer s.sql(t, "DELETE FROM discovery_contexts WHERE agent_id=? AND persistence='saved'", s.owner)
		for _, tc := range []struct {
			need string
			kind discovery.Kind
		}{{"find_info", discovery.Broadcast}, {"find_service", discovery.Commission}, {"find_people", discovery.Agent}} {
			t.Run(string(tc.kind), func(t *testing.T) {
				need := s.saved(t, tc.need)
				r := discovery.Request{NeedIDs: []string{fmt.Sprint(need.ID)}, SourceKinds: []discovery.Kind{tc.kind}}
				key := "recommend-" + string(tc.kind)
				first := s.recommend(t, r, key)
				require.Len(t, first.Items, 1)
				require.Equal(t, tc.kind, first.Items[0].Ref.Type)
				require.Equal(t, need.ID, first.Items[0].NeedID)
				s.waitSamples(t, first.ImpressionID, 1)
				var recorded struct {
					NeedID, NeedRevision int64
					RequestMode          string
				}
				require.NoError(t, s.db.Table("replay_logs").Where("impression_id=?", first.ImpressionID).Take(&recorded).Error)
				require.Equal(t, need.ID, recorded.NeedID)
				require.EqualValues(t, 1, recorded.NeedRevision)
				require.Equal(t, "recommendation", recorded.RequestMode)
				require.Eventually(t, func() bool {
					if tc.kind == discovery.Broadcast {
						return mq.RDB.SIsMember(ctx, fmt.Sprintf(impr.KeyItemIDs, s.owner), fmt.Sprint(s.item)).Val()
					}
					return mq.RDB.SIsMember(ctx, fmt.Sprintf("impr:discovery:agent:%d:items", s.owner), first.Items[0].Ref.Key()).Val()
				}, 3*time.Second, 25*time.Millisecond)
				fresh := s.recommend(t, r, "")
				require.Empty(t, fresh.Items)
				require.Equal(t, "exhausted", fresh.Status)
				s.call(t, "POST", fmt.Sprintf("/api/v2/needs/%d/state", need.ID), s.token, "", map[string]any{"state": "completed", "expected_revision": 1}, 200)
				require.Equal(t, first, s.recommend(t, r, key), "retry must retain its response after the Need closes")
				s.call(t, "POST", "/api/v2/discovery/recommendations", s.token, "new-after-close", r, 409)
				// Search remains repeatable after automatic exposure.
				require.Len(t, s.search(t, discovery.Request{Query: "landing page design", SourceKinds: []discovery.Kind{tc.kind}, Filters: discovery.Filters{Category: s.category}}, "").Items, 1)
			})
		}
	})
	t.Run("NoMatchNeedNeverBroadens", func(t *testing.T) {
		defer s.sql(t, "DELETE FROM discovery_contexts WHERE agent_id=? AND persistence='saved'", s.owner)
		in := s.need("find_service")
		in.Constraints.ProviderRegion = []string{"unknown-region"}
		need := decode[discovery.Context](t, s.call(t, "POST", "/api/v2/needs", s.token, "", in, 200))
		x := s.recommend(t, discovery.Request{NeedIDs: []string{fmt.Sprint(need.ID)}, SourceKinds: []discovery.Kind{discovery.Commission}}, "")
		require.Empty(t, x.Items)
		require.Equal(t, "no_match", x.Status)
		require.Empty(t, x.FallbackReason)
	})
	t.Run("SourceChangesAffectNewRequestsOnly", func(t *testing.T) {
		r := discovery.Request{Query: "landing page design", Filters: discovery.Filters{Category: s.category}}
		before := s.search(t, r, "before-block")
		require.Len(t, before.Items, 3)
		s.sql(t, "INSERT INTO user_relations(from_uid,to_uid,rel_type,created_at) VALUES(?,?,2,?)", s.owner, s.author, time.Now().UnixMilli())
		defer s.sql(t, "DELETE FROM user_relations WHERE from_uid=? AND to_uid=?", s.owner, s.author)
		require.Equal(t, before, s.search(t, r, "before-block"))
		require.Empty(t, s.search(t, r, "after-block").Items)
	})
	t.Run("CatalogueFailureIsNotAnEmptySuccess", func(t *testing.T) {
		r := discovery.Request{Query: "landing page design", SourceKinds: []discovery.Kind{discovery.Commission}, Filters: discovery.Filters{Category: s.category}}
		require.Len(t, s.search(t, r, "").Items, 1)
		s.catalogue.mu.Lock()
		s.catalogue.fail = true
		s.catalogue.mu.Unlock()
		defer func() { s.catalogue.mu.Lock(); s.catalogue.fail = false; s.catalogue.mu.Unlock() }()
		s.call(t, "POST", "/api/v2/discovery/search", s.token, "", r, 503)
	})
}
