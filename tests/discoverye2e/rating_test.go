package discoverye2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"eigenflux_server/pkg/agentidentity"
	embcodec "eigenflux_server/pkg/embedding"
	"eigenflux_server/pkg/es"
	sortdal "eigenflux_server/rpc/sort/dal"
	"eigenflux_server/rpc/sort/discovery"
	"eigenflux_server/rpc/sort/discoverylr"
	"github.com/stretchr/testify/require"
)

// Real HTTP -> Feed -> Sort -> ES/PG with the rating model on: profile
// similarity read through ES, PGC classification read from agents, the
// recorded input vector reproduces the recorded probability, and the page
// follows the model.
func TestDiscoveryRatingRecommendation(t *testing.T) {
	raw, err := os.ReadFile("../../rpc/sort/discoverylr/testdata/discovery_rating_lr_v1.json")
	require.NoError(t, err)
	var fixture struct {
		Model json.RawMessage `json:"model"`
	}
	require.NoError(t, json.Unmarshal(raw, &fixture))
	var model struct {
		Version      string    `json:"model_version"`
		Coefficients []float64 `json:"coefficients"`
		Intercept    float64   `json:"intercept"`
	}
	require.NoError(t, json.Unmarshal(fixture.Model, &model))
	path := filepath.Join(t.TempDir(), "rating.json")
	require.NoError(t, os.WriteFile(path, fixture.Model, 0600))
	s := startStack(t, map[string]string{"DISCOVERY_RATING_LR_BPS": "10000", "DISCOVERY_RATING_LR_MODEL_PATH": path})
	ctx := context.Background()
	now := time.Now()
	query := fmt.Sprintf("ratingtoken%d", s.owner)
	s.changeInputs(t, "UPDATE agent_context_revisions SET compiled_context=?::jsonb WHERE agent_id=?", fmt.Sprintf(`{"intent_actions":[{"watch_for":%q,"trigger_when":""}]}`, query), s.owner)
	s.sql(t, "INSERT INTO agent_profiles(agent_id,status,profile_embedding,updated_at) VALUES(?,3,?,?) ON CONFLICT(agent_id) DO UPDATE SET profile_embedding=EXCLUDED.profile_embedding", s.owner, embcodec.Encode(s.vector), now.UnixMilli())
	publisher := s.item + 60000
	shortID, err := agentidentity.GenerateShortID()
	require.NoError(t, err)
	s.sql(t, "INSERT INTO agents(agent_id,short_id,email,agent_name,agent_name_en,created_at,updated_at,profile_completed_at) VALUES(?,?,?,?,?,?,?,?)", publisher, shortID, fmt.Sprintf("rating-e2e-%d@pgc.eigenflux.one", publisher), fmt.Sprintf("rating-pgc-%d", publisher), "Rating PGC", now.UnixMilli(), now.UnixMilli(), now.UnixMilli())
	orthogonal := make([]float32, len(s.vector))
	orthogonal[1] = 1
	related, unrelated, official := s.item+60001, s.item+60002, s.item+60003
	items := []struct {
		id, author int64
		vector     []float32
	}{{related, s.author, s.vector}, {unrelated, s.author, orthogonal}, {official, publisher, orthogonal}}
	for _, f := range items {
		text := fmt.Sprintf("%s update %d", query, f.id)
		s.sql(t, "INSERT INTO raw_items(item_id,author_agent_id,raw_content,raw_url,created_at) VALUES(?,?,?,'',?)", f.id, f.author, text, now.UnixMilli())
		s.sql(t, "INSERT INTO processed_items(item_id,status,summary,lang,quality_score,group_id,updated_at,broadcast_type,source_type) VALUES(?,3,?,'en',0.8,?,?,'info','original')", f.id, text, f.id, now.UnixMilli())
		require.NoError(t, sortdal.IndexItem(ctx, &sortdal.Item{ID: f.id, AuthorAgentID: f.author, Content: text, Summary: text, Lang: "en", Type: "info", SourceType: "original", QualityScore: .8, GroupID: f.id, CreatedAt: now, UpdatedAt: now, Embedding: f.vector}))
	}
	t.Cleanup(func() {
		ids := []int64{related, unrelated, official}
		s.sql(t, "DELETE FROM processed_items WHERE item_id IN ?", ids)
		s.sql(t, "DELETE FROM raw_items WHERE item_id IN ?", ids)
		s.sql(t, "DELETE FROM agents WHERE agent_id=?", publisher)
		for _, id := range ids {
			r, err := es.Client.DeleteByQuery([]string{es.ReadIndexPattern}, bytes.NewBufferString(fmt.Sprintf(`{"query":{"term":{"id":%d}}}`, id)), es.Client.DeleteByQuery.WithRefresh(true))
			if err == nil {
				r.Body.Close()
			}
			require.NoError(t, err)
		}
	})
	response, err := es.Client.Indices.Refresh(es.Client.Indices.Refresh.WithIndex(es.IndexName))
	require.NoError(t, err)
	response.Body.Close()

	result := s.recommend(t, discovery.Request{SourceKinds: []discovery.Kind{discovery.Broadcast}, Limit: 20}, "rating-lr")
	at := map[int64]int{}
	for i, item := range result.Items {
		at[item.Ref.ID] = i + 1
		require.Equal(t, "positive_rating_probability", item.Match["score_kind"], "%+v", item)
	}
	for _, id := range []int64{related, unrelated, official} {
		require.NotZero(t, at[id], "rule-eligible keyword matches stay eligible: %+v", result)
	}
	require.Less(t, at[related], at[unrelated], "profile similarity orders the page through the model: %+v", result)
	s.waitSamples(t, result.ImpressionID, len(result.Items))
	feature := func(c discovery.Candidate, name string) float64 {
		return c.RatingFeatures[slices.Index(discoverylr.RatingFeatureNames, name)]
	}
	for _, id := range []int64{related, unrelated, official} {
		var raw string
		require.NoError(t, s.db.Table("replay_logs").Select("item_features").Where("impression_id=? AND item_id=?", result.ImpressionID, id).Scan(&raw).Error)
		c := decode[struct {
			Search discovery.Candidate `json:"search"`
		}](t, []byte(raw)).Search
		require.NotNil(t, c.LR)
		require.Equal(t, discoverylr.RatingContract, c.LR.Contract)
		require.Equal(t, model.Version, c.LR.ModelVersion)
		require.Len(t, c.RatingFeatures, len(discoverylr.RatingFeatureNames))
		require.Equal(t, "rule_score", c.Score.Kind, "frozen rule evidence must survive rating and delivery")
		z := model.Intercept
		for i, v := range c.RatingFeatures {
			z += v * model.Coefficients[i]
		}
		require.InDelta(t, 1/(1+math.Exp(-z)), c.LR.Probability, 1e-12, "recorded input must reproduce the recorded probability")
		require.Equal(t, 1.0, feature(c, "o_profile"))
		require.Equal(t, 1.0, feature(c, "ch_lexical"))
		require.Equal(t, 1.0, feature(c, "ct_info"))
		require.Equal(t, 1.0, feature(c, "src_original"))
		require.Equal(t, 1.0, feature(c, "has_prof"))
		require.Equal(t, 0.0, feature(c, "has_need"))
		wantCos, wantPGC := 0.0, 0.0
		if id == related {
			wantCos = 1
		}
		if id == official {
			wantPGC = 1
		}
		require.InDelta(t, wantCos, feature(c, "prof_cos0"), 1e-5, "item %d", id)
		require.Equal(t, wantPGC, feature(c, "is_pgc"), "item %d", id)
	}
}

// With the challenger on for every rating-ranked Agent, the item's real-time
// rating counts are read from item_stats, recorded as the last three inputs,
// and order otherwise identical candidates.
func TestDiscoveryRatingV2Recommendation(t *testing.T) {
	paths := map[string]string{}
	models := map[string]struct {
		Version      string    `json:"model_version"`
		Coefficients []float64 `json:"coefficients"`
		Intercept    float64   `json:"intercept"`
	}{}
	for _, name := range []string{"discovery_rating_lr_v1", "discovery_rating_lr_v2"} {
		raw, err := os.ReadFile("../../rpc/sort/discoverylr/testdata/" + name + ".json")
		require.NoError(t, err)
		var fixture struct {
			Model json.RawMessage `json:"model"`
		}
		require.NoError(t, json.Unmarshal(raw, &fixture))
		m := models[name]
		require.NoError(t, json.Unmarshal(fixture.Model, &m))
		models[name] = m
		paths[name] = filepath.Join(t.TempDir(), name+".json")
		require.NoError(t, os.WriteFile(paths[name], fixture.Model, 0600))
	}
	s := startStack(t, map[string]string{"DISCOVERY_RATING_LR_BPS": "10000", "DISCOVERY_RATING_LR_MODEL_PATH": paths["discovery_rating_lr_v1"],
		"DISCOVERY_RATING_LR_V2_BPS": "10000", "DISCOVERY_RATING_LR_V2_MODEL_PATH": paths["discovery_rating_lr_v2"]})
	ctx := context.Background()
	now := time.Now()
	query := fmt.Sprintf("ratingvtwo%d", s.owner)
	s.changeInputs(t, "UPDATE agent_context_revisions SET compiled_context=?::jsonb WHERE agent_id=?", fmt.Sprintf(`{"intent_actions":[{"watch_for":%q,"trigger_when":""}]}`, query), s.owner)
	s.sql(t, "INSERT INTO agent_profiles(agent_id,status,profile_embedding,updated_at) VALUES(?,3,?,?) ON CONFLICT(agent_id) DO UPDATE SET profile_embedding=EXCLUDED.profile_embedding", s.owner, embcodec.Encode(s.vector), now.UnixMilli())
	liked, disliked := s.item+70001, s.item+70002
	counts := map[int64][4]int64{liked: {0, 1, 7, 2}, disliked: {9, 1, 0, 0}} // -1, 0, 1, 2
	for id, c := range counts {
		text := fmt.Sprintf("%s update %d", query, id)
		s.sql(t, "INSERT INTO raw_items(item_id,author_agent_id,raw_content,raw_url,created_at) VALUES(?,?,?,'',?)", id, s.author, text, now.UnixMilli())
		s.sql(t, "INSERT INTO processed_items(item_id,status,summary,lang,quality_score,group_id,updated_at,broadcast_type,source_type) VALUES(?,3,?,'en',0.8,?,?,'info','original')", id, text, id, now.UnixMilli())
		s.sql(t, "INSERT INTO item_stats(item_id,author_agent_id,score_neg1_count,score_0_count,score_1_count,score_2_count,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)", id, s.author, c[0], c[1], c[2], c[3], now.UnixMilli(), now.UnixMilli())
		require.NoError(t, sortdal.IndexItem(ctx, &sortdal.Item{ID: id, AuthorAgentID: s.author, Content: text, Summary: text, Lang: "en", Type: "info", SourceType: "original", QualityScore: .8, GroupID: id, CreatedAt: now, UpdatedAt: now, Embedding: s.vector}))
	}
	t.Cleanup(func() {
		ids := []int64{liked, disliked}
		s.sql(t, "DELETE FROM item_stats WHERE item_id IN ?", ids)
		s.sql(t, "DELETE FROM processed_items WHERE item_id IN ?", ids)
		s.sql(t, "DELETE FROM raw_items WHERE item_id IN ?", ids)
		for _, id := range ids {
			r, err := es.Client.DeleteByQuery([]string{es.ReadIndexPattern}, bytes.NewBufferString(fmt.Sprintf(`{"query":{"term":{"id":%d}}}`, id)), es.Client.DeleteByQuery.WithRefresh(true))
			if err == nil {
				r.Body.Close()
			}
			require.NoError(t, err)
		}
	})
	response, err := es.Client.Indices.Refresh(es.Client.Indices.Refresh.WithIndex(es.IndexName))
	require.NoError(t, err)
	response.Body.Close()

	result := s.recommend(t, discovery.Request{SourceKinds: []discovery.Kind{discovery.Broadcast}, Limit: 20}, "rating-lr-v2")
	at := map[int64]int{}
	for i, item := range result.Items {
		at[item.Ref.ID] = i + 1
	}
	require.NotZero(t, at[liked], "%+v", result)
	require.NotZero(t, at[disliked], "%+v", result)
	require.Less(t, at[liked], at[disliked], "item rating counts order otherwise identical candidates: %+v", result)
	s.waitSamples(t, result.ImpressionID, len(result.Items))
	model := models["discovery_rating_lr_v2"]
	n := len(discoverylr.RatingFeatureNames)
	for id, c := range counts {
		var raw string
		require.NoError(t, s.db.Table("replay_logs").Select("item_features").Where("impression_id=? AND item_id=?", result.ImpressionID, id).Scan(&raw).Error)
		cand := decode[struct {
			Search discovery.Candidate `json:"search"`
		}](t, []byte(raw)).Search
		require.NotNil(t, cand.LR)
		require.Equal(t, discoverylr.RatingV2Contract, cand.LR.Contract)
		require.Equal(t, model.Version, cand.LR.ModelVersion)
		require.Len(t, cand.RatingFeatures, len(discoverylr.RatingV2FeatureNames))
		total := float64(c[0] + c[1] + c[2] + c[3])
		require.InDelta(t, math.Log1p(total)/math.Log(101), cand.RatingFeatures[n], 1e-12)
		require.InDelta(t, (float64(c[2]+c[3])+1)/(total+3), cand.RatingFeatures[n+1], 1e-12)
		require.InDelta(t, (float64(c[0])+1)/(total+3), cand.RatingFeatures[n+2], 1e-12)
		z := model.Intercept
		for i, v := range cand.RatingFeatures {
			z += v * model.Coefficients[i]
		}
		require.InDelta(t, 1/(1+math.Exp(-z)), cand.LR.Probability, 1e-12)
	}
}
