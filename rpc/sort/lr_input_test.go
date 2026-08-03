package main

import (
	"testing"
	"time"

	"eigenflux_server/pkg/recallsource"
	sortDal "eigenflux_server/rpc/sort/dal"
	"eigenflux_server/rpc/sort/lrranker"
	"eigenflux_server/rpc/sort/ranker"
)

// TestScoreItemsWithLRGlue exercises the handler glue (buildLRInput +
// scoreItemsWithLR) against a real bundle, without any infra. It drives the
// package-global lrManager the handler uses.
func TestScoreItemsWithLRGlue(t *testing.T) {
	prev := lrManager
	lrManager = lrranker.NewManager(lrranker.Config{
		Enabled:        true,
		ModelPath:      "lrranker/testdata/model.json",
		ReloadInterval: time.Hour,
	})
	defer func() { lrManager.Close(); lrManager = prev }()

	if !lrManager.Available() {
		t.Fatal("expected LR model to load from testdata bundle")
	}

	now := time.Now()
	expire := now.Add(48 * time.Hour)
	items := map[int64]sortDal.Item{
		1: {ID: 1, Type: "info", SourceType: "curated", Timeliness: "timely", Lang: "en", Keywords: []string{"ai agents"}, Domains: []string{"tech"}, Geo: "US", QualityScore: 0.8, CreatedAt: now.Add(-2 * time.Hour)},
		2: {ID: 2, Type: "demand", SourceType: "original", Lang: "zh", Keywords: []string{"finance"}, QualityScore: 0.3, CreatedAt: now.Add(-100 * time.Hour), ExpireTime: &expire},
	}
	ranked := []ranker.RankedItem{
		{ItemID: 1, Score: 0.5, Scores: ranker.ScoreBreakdown{Semantic: 0.4, Keyword: 0.5, Freshness: 0.9, Total: 0.5}},
		{ItemID: 2, Score: 0.4, Scores: ranker.ScoreBreakdown{Semantic: 0.2, Keyword: 0.1, Freshness: 0.6, Total: 0.4}},
	}
	sourceMap := map[int64]recallsource.Source{
		1: recallsource.Keyword,
		2: recallsource.NewUGC | recallsource.Keyword,
	}
	profile := &ranker.UserProfile{Keywords: []string{"ai-agents"}, Domains: []string{"tech"}, Geo: "us"}

	out := scoreItemsWithLR(ranked, items, sourceMap, profile)
	if len(out) != 2 {
		t.Fatalf("expected 2 scored items, got %d", len(out))
	}
	for id, res := range out {
		if res.Probability <= 0 || res.Probability >= 1 {
			t.Errorf("item %d probability %g out of (0,1)", id, res.Probability)
		}
		if res.ModelVersion == "" {
			t.Errorf("item %d missing model version", id)
		}
	}
}

func TestBuildLRInputMapsFields(t *testing.T) {
	now := time.Now()
	expire := now.Add(24 * time.Hour)
	created := now.Add(-3 * time.Hour)
	item := sortDal.Item{
		ID: 7, Type: "supply", SourceType: "forwarded", Timeliness: "evergreen", Lang: "zh-CN",
		Keywords: []string{"go"}, Domains: []string{"backend"}, Geo: "CN", QualityScore: 0.6,
		CreatedAt: created, ExpireTime: &expire,
	}
	scores := ranker.ScoreBreakdown{Semantic: 0.1, Keyword: 0.2, Freshness: 0.3, Total: 0.25, IsDraft: true}
	profile := &ranker.UserProfile{Keywords: []string{"go"}, Domains: []string{"backend"}, Geo: "cn"}

	in := buildLRInput(now, profile, item, scores, recallsource.Friend)

	if in.BroadcastType != "supply" || in.Lang != "zh-CN" || in.ItemGeo != "CN" {
		t.Errorf("enum/geo fields not mapped: %+v", in)
	}
	if in.ExpireTimeMS == nil || *in.ExpireTimeMS != expire.UnixMilli() {
		t.Errorf("expire not mapped")
	}
	if in.CreatedAtMS == nil || *in.CreatedAtMS != created.UnixMilli() {
		t.Errorf("created not mapped")
	}
	if in.RankScores.Total == nil || *in.RankScores.Total != 0.25 || !in.RankScores.IsDraft {
		t.Errorf("rank scores not mapped")
	}
	if len(in.RecallSourceNames) != 1 || in.RecallSourceNames[0] != "friend" {
		t.Errorf("recall names not mapped: %v", in.RecallSourceNames)
	}
}
