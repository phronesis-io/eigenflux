package discoverye2e

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"eigenflux_server/pkg/agentcard"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/pkg/need"
	profiledal "eigenflux_server/rpc/profile/dal"
	"eigenflux_server/rpc/sort/discovery"
	"github.com/stretchr/testify/require"
)

func TestDiscoveryContextCacheE2E(t *testing.T) {
	s := startStack(t)
	ctx := context.Background()
	r := discovery.Request{SourceKinds: []discovery.Kind{discovery.Agent}, Limit: 5}
	first := s.recommend(t, r, "")
	require.Equal(t, "insufficient_context", first.Status)
	// Warm both empty Need selection and empty owner context before a real write.
	require.Equal(t, "insufficient_context", s.recommend(t, r, "").Status)
	in := s.need(t, "agent")
	in.Constraints.Lang = []string{"zh"}
	s.capture(t, in)
	constrained := s.recommend(t, r, "")
	require.Equal(t, "need_input", constrained.Origin)
	require.Empty(t, constrained.Items)
	in.Constraints.Lang = nil
	saved := s.capture(t, in)
	fresh := s.recommend(t, r, "")
	require.Len(t, fresh.Items, 1)
	require.Equal(t, saved.NeedInputID, fresh.Items[0].NeedID)
	s.waitSamples(t, fresh.ImpressionID, 1)
	// Direct recommendation calls create distinct executions while sharing plans.
	next := s.recommend(t, r, "")
	require.NotEqual(t, fresh.ContextID, next.ContextID)
	require.Equal(t, "need_input", next.Origin)
	var rows int64
	require.NoError(t, s.db.Table("discovery_contexts").Where("agent_id=?", s.owner).Count(&rows).Error)
	require.Zero(t, rows)

	// An actual Intent mutation invalidates cached selection and owner text.
	s.sql(t, `INSERT INTO agent_context_heads(agent_id,current_revision,active_revision,updated_at) VALUES(?,1,1,?)`, s.owner, time.Now().UnixMilli())
	mutate := func(revision int64, watch string) {
		s.call(t, "PUT", fmt.Sprintf("/api/v2/agent-context/intent-actions/%d", in.IntentID), s.token, "", map[string]any{
			"expected_context_revision": revision, "idempotency_key": fmt.Sprintf("cache-intent-%d", revision),
			"watch_for": watch, "trigger_when": "", "action_instruction": "summarize", "action_policy": "analyze_only", "priority": 10,
		}, 200)
	}
	mutate(1, "quartznebulaunmatched")
	after := s.recommend(t, r, "")
	require.Equal(t, "agent_context", after.Origin)
	require.Equal(t, "no_active_needs", after.FallbackReason)
	require.Empty(t, after.Items)
	s.call(t, "POST", "/api/v2/discovery/search", s.token, "", discovery.Request{NeedID: saved.NeedInputID}, 409)
	mutate(2, "landing page design")
	require.NoError(t, mq.RDB.Del(ctx, fmt.Sprintf("impr:discovery:agent:%d:items", s.owner)).Err())
	after = s.recommend(t, r, "")
	require.Len(t, after.Items, 1)
	require.Equal(t, "agent_context", after.Origin)
	s.waitSamples(t, after.ImpressionID, 1)
	var raw string
	require.NoError(t, s.db.Table("replay_logs").Select("agent_features").Where("impression_id=?", after.ImpressionID).Scan(&raw).Error)
	sample := decode[struct {
		Search struct {
			Contexts []discovery.Context `json:"contexts"`
		} `json:"search_context"`
	}](t, []byte(raw))
	require.Equal(t, "3:1", sample.Search.Contexts[0].SourceRevision)
	require.Equal(t, after.ContextID, sample.Search.Contexts[0].ID)
	require.Equal(t, "landing page design", sample.Search.Contexts[0].Query)

	// Batch capture commits before invalidating a warm automatic selection.
	cr := discovery.Request{SourceKinds: []discovery.Kind{discovery.Commission}, Limit: 5}
	s.recommend(t, cr, "")
	commission := s.need(t, "commission")
	commission.Constraints.Lang = []string{"zh"}
	input, _ := json.Marshal(commission)
	review := need.CaptureReview{IntentID: commission.IntentID, IntentVersion: commission.IntentVersion, Outcome: "captured", Inputs: []json.RawMessage{input}}
	s.call(t, "POST", "/api/v2/need-capture/complete", s.token, "", review, 200)
	require.Equal(t, "need_input", s.recommend(t, cr, "").Origin)

	// A Card projection rebuild invalidates a warm empty owner cache.
	before := decode[discovery.Response](t, s.call(t, "POST", "/api/v2/discovery/recommendations", s.otherToken, "", r, 200))
	require.Equal(t, "insufficient_context", before.Status)
	require.NoError(t, profiledal.EnsureAgentProfileRow(s.db, s.other))
	s.sql(t, `UPDATE agent_profiles SET profile_data='{"seeking":["landing page design"]}'::jsonb, profile_version=profile_version+2 WHERE agent_id=?`, s.other)
	for {
		_, complete, err := agentcard.AdvanceInfluenceRollupBackfill(ctx, s.db, 100)
		require.NoError(t, err)
		if complete {
			break
		}
	}
	require.NoError(t, agentcard.Rebuild(ctx, s.db, mq.RDB, s.other))
	card := decode[discovery.Response](t, s.call(t, "POST", "/api/v2/discovery/recommendations", s.otherToken, "", r, 200))
	require.Equal(t, "agent_context", card.Origin)
	require.NotEmpty(t, card.Items)
}
