package discoverye2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"eigenflux_server/pkg/featureindex"

	"eigenflux_server/pkg/es"
	"eigenflux_server/pkg/impr"
	"eigenflux_server/pkg/mq"
	needmodel "eigenflux_server/pkg/need"
	"eigenflux_server/pkg/replaylog"
	"eigenflux_server/rpc/sort/discovery"
	"eigenflux_server/rpc/sort/discovery/queryprocessing"

	"github.com/stretchr/testify/require"
)

func (s *stack) need(t *testing.T, kind string) discovery.NeedInput {
	t.Helper()
	var intent int64
	require.NoError(t, s.db.Raw(`INSERT INTO agent_intent_actions(agent_id,watch_for,trigger_when,action_instruction,action_policy,priority,source,status,version,created_at,updated_at) VALUES (?, 'landing page design', 'design request', 'summarize', 'analyze_only', 10, 'human_edit', 'active', 1, 1, 1) RETURNING intent_id`, s.owner).Scan(&intent).Error)
	priority := .8
	return discovery.NeedInput{SchemaVersion: needmodel.InputSchemaVersion, IntentID: intent, IntentVersion: 1, NeedType: kind, Priority: &priority, Target: needmodel.Target{Goal: "landing page design"}}
}
func (s *stack) capture(t *testing.T, in discovery.NeedInput) needmodel.Record {
	t.Helper()
	return decode[struct {
		NeedInput needmodel.Record `json:"need_input"`
	}](t, s.call(t, "POST", "/api/v2/need-inputs", s.token, fmt.Sprintf("capture-%d-%d", in.IntentID, time.Now().UnixNano()), in, 201)).NeedInput
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
func (s *stack) saved(t *testing.T, kind string) needmodel.Record {
	t.Helper()
	return s.capture(t, s.need(t, kind))
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
	t.Run("MissingContextReturnsEmptyDiscoveryAndCompleteFeed", func(t *testing.T) {
		s.changeInputs(t, `UPDATE agent_context_revisions SET compiled_context='{"intent_actions":[]}'::jsonb WHERE agent_id=?`, s.other)
		for _, kinds := range [][]discovery.Kind{{discovery.Agent}, {discovery.Commission}, {discovery.Agent, discovery.Commission}} {
			raw := s.call(t, "POST", "/api/v2/discovery/recommendations", s.otherToken, "", discovery.Request{SourceKinds: kinds, Limit: 5}, 200)
			out := decode[discovery.Response](t, raw)
			require.Equal(t, "insufficient_context", out.Status)
			require.NotNil(t, out.Items)
			require.Empty(t, out.Items)
			require.False(t, out.HasMore)
		}
		// No broadcast recall fixture is enabled either: all three lanes may be empty.
		out := decode[discovery.Response](t, s.call(t, "POST", "/api/v2/discovery/recommendations", s.otherToken, "", discovery.Request{Limit: 5}, 200))
		require.NotNil(t, out.Items)
		require.Empty(t, out.Items)
		require.False(t, out.HasMore)
		feed := decode[map[string]any](t, s.call(t, "POST", "/api/v2/feed", s.otherToken, "", map[string]any{"limit": 5}, 200))
		require.Equal(t, "feed.v2", feed["schema_version"])
		require.Equal(t, []any{}, feed["items"])
		for _, field := range []string{"notifications", "cadence", "personalization", "control_context_snapshot", "capabilities_applied", "discovery"} {
			require.Contains(t, feed, field, "empty discovery must not bypass Feed assembly")
		}
		require.Equal(t, "full", feed["personalization"].(map[string]any)["context_delivery"])
		require.Equal(t, map[string]any{"intent_actions": []any{}}, feed["control_context_snapshot"])
	})
	t.Run("PartialNeedCoverageFallsBackOnlyForMissingKinds", func(t *testing.T) {
		defer s.changeInputs(t, "DELETE FROM need_inputs WHERE agent_id=?", s.owner)
		var original string
		require.NoError(t, s.db.Raw("SELECT compiled_context::text FROM agent_context_revisions WHERE agent_id=? AND revision=1", s.owner).Scan(&original).Error)
		defer s.changeInputs(t, "UPDATE agent_context_revisions SET compiled_context=?::jsonb WHERE agent_id=? AND revision=1", original, s.owner)
		s.changeInputs(t, `UPDATE agent_context_revisions SET compiled_context='{"intent_actions":[{"watch_for":"landing page design","trigger_when":"design request"}]}'::jsonb WHERE agent_id=? AND revision=1`, s.owner)
		in := s.need(t, "broadcast")
		in.Constraints.Lang = []string{"zh"}
		s.capture(t, in)
		calls := s.embeddingCallCount()
		out := s.recommend(t, discovery.Request{Limit: 20}, "")
		require.Equal(t, calls, s.embeddingCallCount(), "fallback must not call embedding")
		require.Equal(t, "missing_kind_needs", out.FallbackReason)
		kinds := map[discovery.Kind]bool{}
		for _, item := range out.Items {
			kinds[item.Ref.Type] = true
			require.NotEqual(t, discovery.Broadcast, item.Ref.Type)
		}
		require.True(t, kinds[discovery.Agent])
		require.True(t, kinds[discovery.Commission])
		historyKey := fmt.Sprintf("impr:discovery:agent:%d:items", s.owner)
		for _, item := range out.Items {
			member := item.Ref.Key()
			require.Eventually(t, func() bool { return mq.RDB.SIsMember(ctx, historyKey, member).Val() }, 3*time.Second, 25*time.Millisecond)
			t.Cleanup(func() { require.NoError(t, mq.RDB.SRem(ctx, historyKey, member).Err()) })
		}
	})
	t.Run("LongIntentFallbackPreservesCapturedNeed", func(t *testing.T) {
		t.Cleanup(func() { s.sql(t, "DELETE FROM need_inputs WHERE agent_id=?", s.owner) })
		var original string
		require.NoError(t, s.db.Raw("SELECT compiled_context::text FROM agent_context_revisions WHERE agent_id=? AND revision=1", s.owner).Scan(&original).Error)
		defer s.changeInputs(t, "UPDATE agent_context_revisions SET compiled_context=?::jsonb WHERE agent_id=? AND revision=1", original, s.owner)
		raw, err := json.Marshal(map[string]any{"intent_actions": []map[string]string{{"watch_for": strings.Repeat("研", 1000), "trigger_when": strings.Repeat("究", 1000)}}})
		require.NoError(t, err)
		s.changeInputs(t, "UPDATE agent_context_revisions SET compiled_context=?::jsonb WHERE agent_id=? AND revision=1", string(raw), s.owner)
		s.capture(t, s.need(t, "agent"))
		out := s.recommend(t, discovery.Request{Limit: 20}, "")
		found := false
		for _, item := range out.Items {
			if item.Ref.Type == discovery.Agent {
				found = true
			}
		}
		require.True(t, found, "valid captured Need was lost to another type's long fallback")
		for _, item := range out.Items {
			member := item.Ref.Key()
			if item.Ref.Type == discovery.Broadcast {
				continue
			}
			key := fmt.Sprintf("impr:discovery:agent:%d:items", s.owner)
			require.Eventually(t, func() bool { return mq.RDB.SIsMember(ctx, key, member).Val() }, 3*time.Second, 25*time.Millisecond)
			t.Cleanup(func() { require.NoError(t, mq.RDB.SRem(ctx, key, member).Err()) })
		}
	})
	t.Run("SlowEmbeddingFallsBackBeforeRPCDeadline", func(t *testing.T) {
		before := s.embeddingCallCount()
		s.stallEmbedding.Store(true)
		defer s.stallEmbedding.Store(false)
		started := time.Now()
		out := s.search(t, discovery.Request{Query: "landing page design"}, "")
		require.GreaterOrEqual(t, time.Since(started), 2*time.Second)
		require.Less(t, time.Since(started), 3*time.Second)
		require.Len(t, out.Items, 3)
		require.Contains(t, out.Reasons, "embedding_unavailable")
		require.Greater(t, s.embeddingCallCount(), before)
	})
	t.Run("SavedNeedEmbeddingIsPrecomputedAndReused", func(t *testing.T) {
		defer s.changeInputs(t, "DELETE FROM need_inputs WHERE agent_id=?", s.owner)
		in := s.need(t, "agent")
		in.Target.Goal = fmt.Sprintf("landing page design vectorcase%d", s.owner)
		saved := s.capture(t, in)
		query := queryprocessing.Process(queryprocessing.NeedText(in.Target.Goal, in.Target.Context), queryprocessing.Options{}).Normalized
		t.Cleanup(func() { mq.RDB.Del(ctx, s.needVectors.Key(query)) })
		callCount := func() int32 {
			v, ok := s.embeddingCalls.Load(query)
			if !ok {
				return 0
			}
			return v.(*atomic.Int32).Load()
		}
		cold := s.search(t, discovery.Request{NeedID: saved.NeedInputID}, "")
		require.Contains(t, cold.Reasons, "embedding_pending")
		require.Len(t, cold.Items, 1, "cache miss must retain lexical retrieval")
		require.Zero(t, callCount(), "online request called the model")
		workerCtx, stop := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { defer close(done); s.needWorker.Start(workerCtx) }()
		defer func() {
			stop()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("Need worker did not stop")
			}
		}()
		require.Eventually(t, func() bool { _, err := s.needVectors.Read(ctx, query); return err == nil }, 15*time.Second, 50*time.Millisecond)
		for i := 0; i < 3; i++ {
			warm := s.search(t, discovery.Request{NeedID: saved.NeedInputID}, "")
			require.NotContains(t, warm.Reasons, "embedding_pending")
			require.Len(t, warm.Items, 1)
			require.Contains(t, warm.Items[0].Match["match_types"], "semantic")
		}
		require.EqualValues(t, 1, callCount(), "same Need was embedded on every request")
	})
	t.Run("CapturedNeedInputOwnershipAndLifecycle", func(t *testing.T) {
		defer s.changeInputs(t, "DELETE FROM need_inputs WHERE agent_id=?", s.owner)
		in := s.need(t, "broadcast")
		in.Target.Goal = "  landing   page design  "
		in.Constraints.Lang = []string{"EN"}
		s.call(t, "POST", "/api/v2/need-inputs", "", "capture-test", in, 401)
		readOnly := s.seedSession(t, s.owner, "{context:read}")
		s.call(t, "POST", "/api/v2/need-inputs", readOnly, "capture-test", in, 403)
		created := s.capture(t, in)
		id := created.NeedInputID
		require.Equal(t, "active", created.Status)
		require.True(t, created.Eligible)
		s.call(t, "POST", "/api/v2/discovery/search", s.otherToken, "", discovery.Request{NeedID: id}, 404)
		s.call(t, "POST", "/api/v2/discovery/search", s.token, "", discovery.Request{NeedID: id, SourceKinds: []discovery.Kind{discovery.Agent}}, 400)
		first := s.search(t, discovery.Request{NeedID: id}, "captured-snapshot")
		require.Len(t, first.Items, 1)
		require.Equal(t, id, first.Items[0].NeedID)
		require.NotEqual(t, id, first.ContextID)
		require.Equal(t, "need_input", first.Origin)
		require.Equal(t, []string{"en"}, first.EffectiveFilters.Lang)
		s.waitSamples(t, first.ImpressionID, 1)
		var sample string
		require.NoError(t, s.db.Table("replay_logs").Select("agent_features").Where("impression_id=?", first.ImpressionID).Scan(&sample).Error)
		replay := decode[struct {
			Search struct {
				Contexts []discovery.Context `json:"contexts"`
			} `json:"search_context"`
		}](t, []byte(sample))
		require.Equal(t, id, replay.Search.Contexts[0].CapturedNeed.InputID)
		require.Equal(t, first.ContextID, replay.Search.Contexts[0].ID)
		var writes int64
		require.NoError(t, s.db.Table("discovery_contexts").Where("context_id=?", first.ContextID).Count(&writes).Error)
		require.Zero(t, writes, "executions must not synchronously persist Context rows")
		snapshot := replay.Search.Contexts[0]
		capturedInput := decode[needmodel.Input](t, snapshot.CapturedNeed.Input)
		require.Equal(t, in, capturedInput)
		require.JSONEq(t, string(created.Input), string(snapshot.CapturedNeed.Input))
		var projections int64
		require.NoError(t, s.db.Table("normalized_needs").Where("need_input_id=?", id).Count(&projections).Error)
		require.Zero(t, projections)
		require.Equal(t, first, s.search(t, discovery.Request{NeedID: id}, "captured-snapshot"))
		// Change the human-owned Intent through its real endpoint.
		s.sql(t, "INSERT INTO agent_context_heads(agent_id,current_revision,active_revision,updated_at) VALUES (?,1,1,1) ON CONFLICT DO NOTHING", s.owner)
		s.call(t, "PUT", fmt.Sprintf("/api/v2/agent-context/intent-actions/%d", in.IntentID), s.token, "", map[string]any{"expected_context_revision": 1, "idempotency_key": "discovery-intent-edit", "watch_for": "landing page design", "trigger_when": "design request", "action_instruction": "summarize", "action_policy": "analyze_only", "priority": 10}, 200)
		s.call(t, "POST", "/api/v2/discovery/search", s.token, "", discovery.Request{NeedID: id}, 409)
		s.call(t, "POST", "/api/v2/discovery/recommendations", s.token, "", discovery.Request{NeedIDs: []string{fmt.Sprint(id)}}, 409)
		require.Equal(t, first, s.search(t, discovery.Request{NeedID: id}, "captured-snapshot"))
		in.IntentVersion = 2
		next := s.capture(t, in)
		nextResult := s.search(t, discovery.Request{NeedID: next.NeedInputID}, "")
		require.Len(t, nextResult.Items, 1)
		require.EqualValues(t, 2, nextResult.Items[0].NeedRevision)
	})
	t.Run("CapturedNeedDeadlineConstraintsAndOpenRequirements", func(t *testing.T) {
		defer s.changeInputs(t, "DELETE FROM need_inputs WHERE agent_id=?", s.owner)
		in := s.need(t, "commission")
		expired := int64(1)
		in.Constraints.DeadlineMS = &expired
		captured := s.capture(t, in)
		s.call(t, "POST", "/api/v2/discovery/search", s.token, "", discovery.Request{NeedID: captured.NeedInputID}, 409)
		s.call(t, "POST", "/api/v2/discovery/search", s.token, "", discovery.Request{NeedID: 1}, 404)
		in.Constraints.DeadlineMS = nil
		in.Constraints.Lang = []string{"English", "Klingon-ish"}
		s.call(t, "POST", "/api/v2/need-inputs", s.token, "invalid-language-codes", in, 400)
		in.Constraints.Lang = nil
		in.Requirements = []needmodel.Condition{{Text: "Do not upload production data"}}
		captured = s.capture(t, in)
		result := s.search(t, discovery.Request{NeedID: captured.NeedInputID}, "")
		require.Len(t, result.Items, 1)
		require.Equal(t, captured.NeedInputID, result.Items[0].NeedID)
		require.NotContains(t, result.Reasons, "unverified_need_requirements")
		other := s.saved(t, "agent")
		mixed := s.recommend(t, discovery.Request{SourceKinds: []discovery.Kind{discovery.Commission, discovery.Agent}}, "")
		require.Len(t, mixed.Items, 2)
		require.ElementsMatch(t, []int64{captured.NeedInputID, other.NeedInputID}, []int64{mixed.Items[0].NeedID, mixed.Items[1].NeedID})
		require.NotContains(t, mixed.Reasons, "unverified_need_requirements")
		historyKey := fmt.Sprintf("impr:discovery:agent:%d:items", s.owner)
		for _, item := range mixed.Items {
			historyMember := item.Ref.Key()
			require.Eventually(t, func() bool {
				return mq.RDB.SIsMember(ctx, historyKey, historyMember).Val()
			}, 3*time.Second, 25*time.Millisecond)
			t.Cleanup(func() {
				require.NoError(t, mq.RDB.SRem(ctx, historyKey, historyMember).Err())
			})
		}
		s.call(t, "POST", "/api/v2/discovery/search", s.otherToken, "", discovery.Request{Need: &in}, 409)
	})
	t.Run("SearchCursorFreezesRankingAndBindsRequest", func(t *testing.T) {
		r := discovery.Request{Query: "landing page design", Filters: discovery.Filters{}, Limit: 2}
		first := s.search(t, r, "paged-search")
		require.Len(t, first.Items, 2)
		require.True(t, first.HasMore)
		require.NotEmpty(t, first.NextCursor)
		s.waitSamples(t, first.ImpressionID, 2)
		r.Cursor = first.NextCursor
		s.call(t, "POST", "/api/v2/discovery/search", s.otherToken, "", r, 410)
		changed := r
		changed.Limit = 3
		s.call(t, "POST", "/api/v2/discovery/search", s.token, "", changed, 409)
		// Index changes do not reshuffle a ranked search already in progress.
		s.sql(t, "UPDATE agents SET agent_name='changed after search' WHERE agent_id=?", s.author)
		defer s.sql(t, "UPDATE agents SET agent_name=? WHERE agent_id=?", fmt.Sprintf("精确查找-%d", s.author), s.author)
		next := s.search(t, r, "")
		require.Len(t, next.Items, 1)
		require.Equal(t, discovery.Agent, next.Items[0].Ref.Type)
		require.Equal(t, first.ImpressionID, next.ImpressionID)
		require.False(t, next.HasMore)
		require.Empty(t, next.NextCursor)
		require.Equal(t, next, s.search(t, r, ""))
		s.waitSamples(t, first.ImpressionID, 3)
		var positions []int
		require.NoError(t, s.db.Table("replay_logs").Where("impression_id=?", first.ImpressionID).Order("position").Pluck("position", &positions).Error)
		require.Equal(t, []int{0, 1, 2}, positions)
	})
	t.Run("ThreeKindSearchTypedSamplesAndIdempotency", func(t *testing.T) {
		r := discovery.Request{Query: "landing page design", Filters: discovery.Filters{}}
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
			require.NotEmpty(t, first.Items[i].Preview["text"], "slim forward retains response preview")
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
	t.Run("FeatureMetricsAreExposedBySort", func(t *testing.T) {
		result := s.search(t, discovery.Request{Query: "landing page"}, "")
		require.Len(t, result.Items, 3)
		port, err := strconv.Atoi(os.Getenv("SORT_RPC_PORT"))
		require.NoError(t, err)
		response, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/metrics", port+1000))
		require.NoError(t, err)
		defer response.Body.Close()
		require.Equal(t, http.StatusOK, response.StatusCode)
		raw, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		for _, name := range []string{"feature_index_read_items_total"} {
			require.Contains(t, string(raw), name)
		}
		require.Contains(t, string(raw), `outcome="request_hit"`)
		for _, name := range []string{"feature_index_operation_seconds", "feature_index_batch_size", "feature_index_payload_bytes", "feature_index_audit_key_bytes", "feature_index_loader_cycle_seconds"} {
			require.NotContains(t, string(raw), name)
		}
		for _, kind := range []string{"agent.card", "broadcast.item", "commission.catalogue", "commission.statistics"} {
			require.Contains(t, string(raw), `view="`+kind+`"`)
		}
	})
	t.Run("TextExclusionsWithSlimForward", func(t *testing.T) {
		for _, tc := range []struct {
			term  string
			count int
		}{{"design", 0}, {"sign", 3}, {"ＤＥＳＩＧＮ", 0}, {"不存在的词", 3}} {
			result := s.search(t, discovery.Request{Query: "landing page", Filters: discovery.Filters{ExcludeTerms: []string{tc.term}}}, "")
			require.Len(t, result.Items, tc.count, tc.term)
		}
	})
	t.Run("QueryNormalizationAndDenseRetrievalWithoutAliases", func(t *testing.T) {
		for _, query := range []string{"  ＬＰ  ", "着陆页", "帮我做LP"} {
			t.Run(query, func(t *testing.T) {
				result := s.search(t, discovery.Request{Query: query}, "")
				require.Len(t, result.Items, 3)
				for _, item := range result.Items {
					require.Contains(t, item.Match["match_types"], "semantic")
					require.NotContains(t, item.Match["match_types"], "synonym")
					require.NotContains(t, item.Match["match_types"], "keyword", "fixture content has no lexical match")
				}
				s.waitSamples(t, result.ImpressionID, 3)
				var raw string
				require.NoError(t, s.db.Table("replay_logs").Select("agent_features").Where("impression_id=?", result.ImpressionID).Limit(1).Scan(&raw).Error)
				sample := decode[struct {
					Search struct {
						Contexts []discovery.Context `json:"contexts"`
					} `json:"search_context"`
				}](t, []byte(raw))
				require.Len(t, sample.Search.Contexts, 1)
				compiled := sample.Search.Contexts[0]
				require.Equal(t, strings.TrimSpace(query), compiled.Query)
				require.Equal(t, queryprocessing.Version, compiled.QueryAnalysis.Version)
				if query == "  ＬＰ  " {
					require.Equal(t, "lp", compiled.QueryAnalysis.Normalized)
				}
			})
		}
		filtered := s.search(t, discovery.Request{Query: "着陆页", Filters: discovery.Filters{Lang: []string{"zh"}}}, "")
		require.Empty(t, filtered.Items, "dense retrieval must not weaken explicit language filters")
	})
	t.Run("RemovedTaxonomyAndAliasesAreNotAvailable", func(t *testing.T) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url+"/api/v2/taxonomy/search?query=design", nil)
		require.NoError(t, err)
		request.Header.Set("Authorization", "Bearer "+s.token)
		response, err := http.DefaultClient.Do(request)
		require.NoError(t, err)
		response.Body.Close()
		require.Equal(t, http.StatusNotFound, response.StatusCode)
		for _, field := range []string{"category", "subtype", "intents", "taxonomy_version"} {
			value := any("design")
			if field == "intents" {
				value = []string{"design"}
			}
			s.call(t, "POST", "/api/v2/discovery/search", s.token, "", map[string]any{"query": "design", "filters": map[string]any{field: value}}, 400)
		}
		result := s.search(t, discovery.Request{Query: "著陸頁"}, "")
		require.Empty(t, result.Items, "no embedding or lexical match must not expand aliases")
		require.Contains(t, result.Reasons, "embedding_unavailable")
	})
	t.Run("NeedQueriesUseSharedProcessing", func(t *testing.T) {
		defer s.changeInputs(t, "DELETE FROM need_inputs WHERE agent_id=?", s.owner)
		for _, kind := range discovery.AllKinds {
			for _, query := range []string{"  ＬＡＮＤＩＮＧ  ", "landing page", "landing设计"} {
				t.Run(string(kind)+"/"+query, func(t *testing.T) {
					in := s.need(t, string(kind))
					in.Target = needmodel.Target{Goal: query}
					in.Requirements = []needmodel.Condition{{Text: "Keep data local"}}
					captured := s.capture(t, in)
					for _, request := range []discovery.Request{{NeedID: captured.NeedInputID}, {Need: &in}} {
						result := s.search(t, request, "")
						require.Len(t, result.Items, 1)
						require.Equal(t, kind, result.Items[0].Ref.Type)
						require.Contains(t, result.Items[0].Match["match_types"], "keyword")
						s.waitSamples(t, result.ImpressionID, 1)
						var raw string
						require.NoError(t, s.db.Table("replay_logs").Select("agent_features").Where("impression_id=?", result.ImpressionID).Limit(1).Scan(&raw).Error)
						sample := decode[struct {
							Search struct {
								Contexts []discovery.Context `json:"contexts"`
							} `json:"search_context"`
						}](t, []byte(raw))
						require.Len(t, sample.Search.Contexts, 1)
						compiled := sample.Search.Contexts[0]
						require.NotNil(t, compiled.QueryAnalysis)
						original, err := compiled.CapturedNeed.ExecutionInput()
						require.NoError(t, err)
						require.Equal(t, in, original)
					}
				})
			}
		}
	})
	t.Run("BroadcastForwardFeaturesAndAuthoritativeState", func(t *testing.T) {
		store := featureindex.BroadcastIndex{DB: s.db, Redis: mq.RDB}
		rows, err := store.Read(ctx, []int64{s.item})
		require.NoError(t, err)
		d := rows[s.item]
		f := (featureindex.BroadcastIndex{Redis: mq.RDB}).Forward()
		defer func() { _, err := store.Load(ctx, []int64{s.item}); require.NoError(t, err) }()
		d.QualityScore = .23
		d.Version, err = f.AllocateVersion(ctx)
		require.NoError(t, err)
		require.NoError(t, f.Put(ctx, s.item, "item", d.Version, d))
		result := s.search(t, discovery.Request{Query: "landing page design", SourceKinds: []discovery.Kind{discovery.Broadcast}}, "")
		require.Len(t, result.Items, 1)
		s.waitSamples(t, result.ImpressionID, 1)
		var raw string
		require.NoError(t, s.db.Table("replay_logs").Select("item_features").Where("impression_id=?", result.ImpressionID).Scan(&raw).Error)
		sample := decode[struct {
			Search discovery.Candidate `json:"search"`
		}](t, []byte(raw))
		require.InDelta(t, .23, sample.Search.Score.Features["quality"], .00001, "quality comes from Redis, not ES or DB")
		require.InDelta(t, 1, sample.Search.Score.Features["cosine"], .00001)
		require.NotContains(t, mq.RDB.HGet(ctx, f.Key(s.item, "item"), "data").Val(), `"embedding"`)
		s.sql(t, "UPDATE processed_items SET status=2 WHERE item_id=?", s.item)
		defer s.sql(t, "UPDATE processed_items SET status=3 WHERE item_id=?", s.item)
		result = s.search(t, discovery.Request{Query: "landing page design", SourceKinds: []discovery.Kind{discovery.Broadcast}}, "")
		require.Empty(t, result.Items, "warm forward projection cannot bypass current item status")
	})
	t.Run("RedisForwardSuppliesRankingFeatures", func(t *testing.T) {
		agentRows, err := (featureindex.AgentIndex{Redis: mq.RDB, IndexName: s.agentIndex}).Read(ctx, []int64{s.author})
		require.NoError(t, err)
		a := agentRows[s.author]
		require.Empty(t, a.Embedding)
		require.NotContains(t, mq.RDB.HGet(ctx, (featureindex.AgentIndex{Redis: mq.RDB, IndexName: s.agentIndex}).Forward().Key(s.author, "card"), "data").Val(), `"embedding"`)
		a.ActivityAt = 0 // The DB public Card still has a recent last_active_at.
		require.NoError(t, (featureindex.AgentIndex{Redis: mq.RDB, IndexName: s.agentIndex}).Write(ctx, a))
		result := s.search(t, discovery.Request{Query: "landing page design", SourceKinds: []discovery.Kind{discovery.Agent}}, "")
		require.Len(t, result.Items, 1)
		s.waitSamples(t, result.ImpressionID, 1)
		var raw string
		require.NoError(t, s.db.Table("replay_logs").Select("item_features").Where("impression_id=?", result.ImpressionID).Scan(&raw).Error)
		sample := decode[struct {
			Search discovery.Candidate `json:"search"`
		}](t, []byte(raw))
		require.Zero(t, sample.Search.Score.Features["activity_freshness"])
		require.InDelta(t, 1, sample.Search.Score.Features["cosine"], 0.00001, "cosine must derive from ES kNN score without a Redis vector")
		require.NotContains(t, mq.RDB.HGet(ctx, (featureindex.CommissionIndex{Redis: mq.RDB, IndexName: s.commissionIndex}).Forward().Key(s.item, "catalogue"), "data").Val(), `"embedding"`)
		before := s.esDocument(t, s.commissionIndex, s.item)
		require.NoError(t, (featureindex.CommissionIndex{Redis: mq.RDB, IndexName: s.commissionIndex}).WriteStatistics(ctx, featureindex.CommissionStatisticsSnapshot{CommissionID: s.item, StatisticsVersion: 7, CompletionRateBPS: 8000}))
		result = s.search(t, discovery.Request{Query: "landing page design", SourceKinds: []discovery.Kind{discovery.Commission}}, "")
		require.Len(t, result.Items, 1)
		s.waitSamples(t, result.ImpressionID, 1)
		require.NoError(t, s.db.Table("replay_logs").Select("item_features").Where("impression_id=?", result.ImpressionID).Scan(&raw).Error)
		sample = decode[struct {
			Search discovery.Candidate `json:"search"`
		}](t, []byte(raw))
		require.InDelta(t, .8, sample.Search.Score.Features["fulfillment"], .00001)
		require.EqualValues(t, 7, sample.Search.Document.StatisticsVersion)
		after := s.esDocument(t, s.commissionIndex, s.item)
		require.Equal(t, before["_version"], after["_version"], "statistics updates must not rewrite ES")
		for _, tc := range []struct {
			index     string
			id        int64
			forbidden []string
		}{
			{s.agentIndex, s.author, []string{"activity_at", "updated_at"}},
			{s.commissionIndex, s.item, []string{"completion_rate_bps", "average_rating_milli", "statistics_version", "updated_at"}},
		} {
			source := s.esDocument(t, tc.index, tc.id)["_source"].(map[string]any)
			for _, field := range tc.forbidden {
				require.NotContains(t, source, field)
			}
		}
	})
	t.Run("ForwardMissVersionGapAndReadFailure", func(t *testing.T) {
		for _, kind := range []discovery.Kind{discovery.Agent, discovery.Commission} {
			t.Run(string(kind), func(t *testing.T) {
				key, versionField := (featureindex.AgentIndex{Redis: mq.RDB, IndexName: s.agentIndex}).Forward().Key(s.author, "card"), "projection_version"
				if kind == discovery.Commission {
					key, versionField = (featureindex.CommissionIndex{Redis: mq.RDB, IndexName: s.commissionIndex}).Forward().Key(s.item, "catalogue"), "catalogue_version"
				}
				original, err := mq.RDB.HGetAll(ctx, key).Result()
				require.NoError(t, err)
				require.NotEmpty(t, original)
				t.Cleanup(func() {
					require.NoError(t, mq.RDB.Del(ctx, key).Err())
					require.NoError(t, mq.RDB.HSet(ctx, key, original).Err())
				})
				r := discovery.Request{Query: "landing page design", SourceKinds: []discovery.Kind{kind}}
				require.NoError(t, mq.RDB.Del(ctx, key).Err())
				require.Empty(t, s.search(t, r, "").Items)
				if kind == discovery.Agent {
					require.Len(t, s.search(t, discovery.Request{Query: fmt.Sprint(s.author), SourceKinds: []discovery.Kind{kind}}, "").Items, 1)
				}
				data := decode[map[string]json.RawMessage](t, []byte(original["data"]))
				data[versionField] = json.RawMessage(`999`)
				b, err := json.Marshal(data)
				require.NoError(t, err)
				require.NoError(t, mq.RDB.HSet(ctx, key, "version", "999", "data", string(b)).Err())
				require.Empty(t, s.search(t, r, "").Items, "ES and forward generations must join")
				require.NoError(t, mq.RDB.Del(ctx, key).Err())
				require.NoError(t, mq.RDB.Set(ctx, key, "wrong type", 0).Err())
				s.call(t, "POST", "/api/v2/discovery/search", s.token, "", r, 503)
			})
		}
	})
	t.Run("ExactCommissionUsesUnifiedDelivery", func(t *testing.T) {
		before := s.embeddingCallCount()
		r := discovery.Request{CommissionID: s.item}
		x := s.search(t, r, "commission-exact-retry")
		require.Len(t, x.Items, 1)
		require.Equal(t, discovery.SourceRef{Type: discovery.Commission, ID: s.item}, x.Items[0].Ref)
		require.Equal(t, "commission_id", x.Items[0].Match["exact"])
		require.Equal(t, "exact_match", x.Items[0].Match["score_kind"])
		require.Equal(t, discovery.PipelineVersion, x.PipelineVersion)
		require.False(t, x.HasMore)
		require.Equal(t, x, s.search(t, r, "commission-exact-retry"))
		s.waitSamples(t, x.ImpressionID, 1)
		for _, id := range []int64{s.item + 100, 9223372036854775807} {
			require.Empty(t, s.search(t, discovery.Request{CommissionID: id}, "").Items)
		}
		minPrice, duration := int64(1), int64(999)
		for _, f := range []discovery.Filters{{Currency: "CNY", MinPriceFen: &minPrice}, {MaxDurationMS: &duration}, {Currency: "USD"}, {ExcludeAuthors: []string{fmt.Sprint(s.author)}}} {
			r.Filters = f
			require.Empty(t, s.search(t, r, "").Items)
		}
		compat := decode[struct {
			Impression string `json:"impression_id"`
			Candidates []struct {
				ID       string `json:"commission_id"`
				Features string `json:"features"`
			} `json:"candidates"`
		}](t, s.call(t, "GET", fmt.Sprintf("/api/v2/commissions/search?commission_id=%d", s.item), s.token, "commission-compat-exact", nil, 200))
		require.Len(t, compat.Candidates, 1)
		require.Equal(t, fmt.Sprint(s.item), compat.Candidates[0].ID)
		require.Contains(t, compat.Candidates[0].Features, "commission_identity_v1")
		s.waitSamples(t, compat.Impression, 1)
		require.Equal(t, before, s.embeddingCallCount(), "exact Commission lookup must not call embedding")
	})
	t.Run("ExactAgentIdentityAndNames", func(t *testing.T) {
		for _, id := range []int64{s.author, s.other} {
			// The second Agent has no ES document. Exact lookup must still work.
			for _, tc := range []struct{ query, match string }{
				{fmt.Sprint(id), "agent_id"}, {s.shortIDs[id], "short_id"},
				{fmt.Sprintf("精确查找-%d", id), "name"}, {fmt.Sprintf("Exact designer %d", id), "name"},
			} {
				x := s.search(t, discovery.Request{Query: "  " + tc.query + "  ", SourceKinds: []discovery.Kind{discovery.Agent}}, "")
				require.Len(t, x.Items, 1, tc.query)
				require.Equal(t, id, x.Items[0].Ref.ID)
				require.Equal(t, tc.match, x.Items[0].Match["exact"])
				require.Equal(t, "exact_match", x.Items[0].Match["score_kind"])
				require.Equal(t, fmt.Sprintf("精确查找-%d", id), x.Items[0].Preview["text"])
				require.False(t, x.Partial)
			}
		}
		for _, q := range []string{fmt.Sprint(s.owner), "9223372036854775807", "99999999999999999999999", "0"} {
			require.Empty(t, s.search(t, discovery.Request{Query: q, SourceKinds: []discovery.Kind{discovery.Agent}}, "").Items)
		}
		// Five-letter text is ambiguous: an unrecognized ID still permits normal
		// text retrieval, but must never be tagged as an exact short-ID match.
		wrongCase := strings.Map(func(r rune) rune {
			if r >= 'a' && r <= 'z' {
				return r - 'a' + 'A'
			}
			return r - 'A' + 'a'
		}, s.shortIDs[s.author])
		for _, item := range s.search(t, discovery.Request{Query: wrongCase, SourceKinds: []discovery.Kind{discovery.Agent}}, "").Items {
			require.NotEqual(t, "short_id", item.Match["exact"])
		}
		name := fmt.Sprintf("精确查找-%d", s.author)
		s.sql(t, "UPDATE agents SET agent_name=? WHERE agent_id=?", name, s.other)
		defer s.sql(t, "UPDATE agents SET agent_name=? WHERE agent_id=?", fmt.Sprintf("精确查找-%d", s.other), s.other)
		sameName := s.search(t, discovery.Request{Query: name, SourceKinds: []discovery.Kind{discovery.Agent}}, "")
		require.Len(t, sameName.Items, 2)
		for _, item := range sameName.Items {
			require.Equal(t, "name", item.Match["exact"])
		}
		r := discovery.Request{Query: fmt.Sprint(s.author), SourceKinds: []discovery.Kind{discovery.Agent}, Filters: discovery.Filters{Lang: []string{"zh"}}}
		require.Empty(t, s.search(t, r, "").Items, "identity lookup must respect explicit filters")
		r.Filters = discovery.Filters{}
		s.sql(t, "INSERT INTO user_relations(from_uid,to_uid,rel_type,created_at) VALUES(?,?,2,?)", s.owner, s.author, time.Now().UnixMilli())
		defer s.sql(t, "DELETE FROM user_relations WHERE from_uid=? AND to_uid=?", s.owner, s.author)
		require.Empty(t, s.search(t, r, "").Items, "identity lookup must respect blocks")
	})
	t.Run("ExactMatchesStayInsideTypeBlocksAndFrozenPages", func(t *testing.T) {
		query := "landing page design"
		for _, id := range []int64{s.author, s.other} {
			s.sql(t, "UPDATE agents SET agent_name=? WHERE agent_id=?", query, id)
			t.Cleanup(func() {
				s.sql(t, "UPDATE agents SET agent_name=? WHERE agent_id=?", fmt.Sprintf("精确查找-%d", id), id)
			})
		}
		request := discovery.Request{Query: query, Limit: 3}
		first := s.search(t, request, "type-block-first-page")
		require.Len(t, first.Items, 3)
		require.True(t, first.HasMore)
		for i, kind := range discovery.AllKinds {
			require.Equal(t, kind, first.Items[i].Ref.Type)
		}
		require.Equal(t, "name", first.Items[2].Match["exact"])
		require.Equal(t, first, s.search(t, request, "type-block-first-page"))
		request.Cursor = first.NextCursor
		second := s.search(t, request, "")
		require.Len(t, second.Items, 1)
		require.Equal(t, discovery.Agent, second.Items[0].Ref.Type)
		require.Equal(t, "name", second.Items[0].Match["exact"])
		require.Less(t, first.Items[2].Ref.ID, second.Items[0].Ref.ID)
		require.False(t, second.HasMore)
		require.Equal(t, first.ImpressionID, second.ImpressionID)
		require.Equal(t, second, s.search(t, request, ""))
		s.waitSamples(t, first.ImpressionID, 4)
		var ids []int64
		require.NoError(t, s.db.Table("replay_logs").Where("impression_id=?", first.ImpressionID).Order("position").Pluck("source_id", &ids).Error)
		require.Equal(t, []int64{s.item, s.item, first.Items[2].Ref.ID, second.Items[0].Ref.ID}, ids)
		one := s.search(t, discovery.Request{Query: query, Limit: 1}, "")
		require.Equal(t, first.Items[0].Ref, one.Items[0].Ref)
		custom := s.search(t, discovery.Request{Query: query, SourceKinds: []discovery.Kind{discovery.Agent, discovery.Commission, discovery.Broadcast}, Limit: 4}, "")
		require.Len(t, custom.Items, 4)
		for i, kind := range []discovery.Kind{discovery.Agent, discovery.Agent, discovery.Commission, discovery.Broadcast} {
			require.Equal(t, kind, custom.Items[i].Ref.Type)
		}
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
		r.Filters = discovery.Filters{Lang: []string{"zh"}}
		r.SourceKinds = []discovery.Kind{discovery.Broadcast}
		require.Empty(t, s.search(t, r, "").Items)
		r.Filters = discovery.Filters{BudgetMaxFen: &zero, Currency: "CNY"}
		s.call(t, "POST", "/api/v2/discovery/search", s.token, "", r, 400)
		need := s.need(t, "agent")
		result := s.search(t, discovery.Request{Need: &need}, "")
		require.Len(t, result.Items, 1)
		require.Equal(t, discovery.Agent, result.Items[0].Ref.Type)
		require.Equal(t, "inline_need", result.Origin)
		var savedCount int64
		require.NoError(t, s.db.Table("discovery_contexts").Where("agent_id=? AND persistence='saved'", s.owner).Count(&savedCount).Error)
		require.Zero(t, savedCount)
	})
	t.Run("CurrentAgentAccountStateOverridesForwardProjection", func(t *testing.T) {
		var completed int64
		require.NoError(t, s.db.Table("agents").Select("profile_completed_at").Where("agent_id=?", s.author).Scan(&completed).Error)
		require.Positive(t, completed)
		s.sql(t, "UPDATE agents SET profile_completed_at=0 WHERE agent_id=?", s.author)
		defer s.sql(t, "UPDATE agents SET profile_completed_at=? WHERE agent_id=?", completed, s.author)
		r := discovery.Request{Query: "landing page design", SourceKinds: []discovery.Kind{discovery.Agent}}
		require.Empty(t, s.search(t, r, "").Items)
	})
	t.Run("KnownAgentRemainsSearchableButIsNotRecommended", func(t *testing.T) {
		need := s.saved(t, "agent")
		defer s.changeInputs(t, "DELETE FROM need_inputs WHERE need_input_id=?", need.NeedInputID)
		s.sql(t, "INSERT INTO user_relations(from_uid,to_uid,rel_type,created_at) VALUES(?,?,1,?)", s.owner, s.author, time.Now().UnixMilli())
		defer s.sql(t, "DELETE FROM user_relations WHERE from_uid=? AND to_uid=?", s.owner, s.author)
		r := discovery.Request{SourceKinds: []discovery.Kind{discovery.Agent}}
		// Omitted Need IDs must select the active saved Need automatically.
		recommended := s.recommend(t, r, "")
		require.Empty(t, recommended.Items)
		require.Equal(t, "no_match", recommended.Status)
		require.Positive(t, recommended.ContextID)
		require.NotEqual(t, need.NeedInputID, recommended.ContextID)
		require.Empty(t, recommended.FallbackReason)
		r.Query = "landing page design"
		r.Filters.Lang = []string{"en"}
		found := s.search(t, r, "")
		require.Len(t, found.Items, 1)
		require.Equal(t, s.author, found.Items[0].Ref.ID)
	})
	t.Run("RecommendationNeedDedupAndFrozenRetry", func(t *testing.T) {
		defer s.changeInputs(t, "DELETE FROM need_inputs WHERE agent_id=?", s.owner)
		for _, tc := range []struct {
			need string
			kind discovery.Kind
		}{{"broadcast", discovery.Broadcast}, {"commission", discovery.Commission}, {"agent", discovery.Agent}} {
			t.Run(string(tc.kind), func(t *testing.T) {
				need := s.saved(t, tc.need)
				r := discovery.Request{NeedIDs: []string{fmt.Sprint(need.NeedInputID)}, SourceKinds: []discovery.Kind{tc.kind}}
				key := "recommend-" + string(tc.kind)
				first := s.recommend(t, r, key)
				require.Len(t, first.Items, 1)
				require.Equal(t, tc.kind, first.Items[0].Ref.Type)
				require.Equal(t, need.NeedInputID, first.Items[0].NeedID)
				s.waitSamples(t, first.ImpressionID, 1)
				var recorded struct {
					NeedID, NeedRevision int64
					RequestMode          string
				}
				require.NoError(t, s.db.Table("replay_logs").Where("impression_id=?", first.ImpressionID).Take(&recorded).Error)
				require.Equal(t, need.NeedInputID, recorded.NeedID)
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
				s.changeInputs(t, "UPDATE agent_intent_actions SET status='deleted',version=version+1 WHERE agent_id=? AND intent_id=?", s.owner, need.IntentID)
				require.Equal(t, first, s.recommend(t, r, key), "retry must retain its response after the Need closes")
				s.call(t, "POST", "/api/v2/discovery/recommendations", s.token, "new-after-close", r, 409)
				// Search remains repeatable after automatic exposure.
				require.Len(t, s.search(t, discovery.Request{Query: "landing page design", SourceKinds: []discovery.Kind{tc.kind}, Filters: discovery.Filters{}}, "").Items, 1)
			})
		}
	})
	t.Run("NoMatchNeedNeverBroadens", func(t *testing.T) {
		defer s.changeInputs(t, "DELETE FROM need_inputs WHERE agent_id=?", s.owner)
		in := s.need(t, "commission")
		in.Constraints.ProviderRegion = []string{"CN"}
		need := s.capture(t, in)
		x := s.recommend(t, discovery.Request{NeedIDs: []string{fmt.Sprint(need.NeedInputID)}, SourceKinds: []discovery.Kind{discovery.Commission}}, "")
		require.Empty(t, x.Items)
		require.Equal(t, "no_match", x.Status)
		require.Empty(t, x.FallbackReason)
	})
	t.Run("SourceChangesAffectNewRequestsOnly", func(t *testing.T) {
		r := discovery.Request{Query: "landing page design", Filters: discovery.Filters{}}
		before := s.search(t, r, "before-block")
		require.Len(t, before.Items, 3)
		s.sql(t, "INSERT INTO user_relations(from_uid,to_uid,rel_type,created_at) VALUES(?,?,2,?)", s.owner, s.author, time.Now().UnixMilli())
		defer s.sql(t, "DELETE FROM user_relations WHERE from_uid=? AND to_uid=?", s.owner, s.author)
		require.Equal(t, before, s.search(t, r, "before-block"))
		require.Empty(t, s.search(t, r, "after-block").Items)
	})
	t.Run("OnlineRankingDoesNotCallCatalogueRPC", func(t *testing.T) {
		r := discovery.Request{Query: "landing page design", SourceKinds: []discovery.Kind{discovery.Commission}, Filters: discovery.Filters{}}
		require.Len(t, s.search(t, r, "").Items, 1)
		s.catalogue.mu.Lock()
		s.catalogue.fail = true
		s.catalogue.mu.Unlock()
		defer func() { s.catalogue.mu.Lock(); s.catalogue.fail = false; s.catalogue.mu.Unlock() }()
		require.Len(t, s.search(t, r, "").Items, 1)
	})
}

func (s *stack) esDocument(t *testing.T, index string, id int64) map[string]any {
	t.Helper()
	response, err := es.Client.Get(index, fmt.Sprint(id))
	require.NoError(t, err)
	defer response.Body.Close()
	require.False(t, response.IsError())
	var value map[string]any
	require.NoError(t, json.NewDecoder(response.Body).Decode(&value))
	return value
}

func (s *stack) embeddingCallCount() int32 {
	var total int32
	s.embeddingCalls.Range(func(_, value any) bool { total += value.(*atomic.Int32).Load(); return true })
	return total
}
