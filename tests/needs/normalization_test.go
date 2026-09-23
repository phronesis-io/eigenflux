package needs_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"eigenflux_server/pkg/need"
)

func createNormalized(t *testing.T, h *harness, key string) need.Record {
	t.Helper()
	r, _, err := (need.Store{DB: h.db, IDs: h.ids}).Create(context.Background(), h.owner, key, []byte(strings.Replace(original, "INTENT_ID", fmt.Sprint(h.intent), 1)), 1)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "normalized" || r.NormalizedNeed == nil || !r.NormalizedNeed.Eligible || r.NormalizedNeed.MappingStatus != need.MappingUnmapped {
		t.Fatalf("input is not immediately usable: %+v", r)
	}
	return r
}

func TestOfflineEnrichmentDoesNotBlockOnlineReadsOrCapture(t *testing.T) {
	h := setup(t)
	r := createNormalized(t, h, "online-independent")
	tx := h.db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	// Simulate a stalled offline publisher with an uncommitted replacement.
	if err := tx.Exec("UPDATE normalized_needs SET status='superseded' WHERE need_input_id=?", r.NeedInputID).Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	store := need.Store{DB: h.db, IDs: h.ids}
	got, err := store.Get(ctx, h.owner, r.NeedInputID)
	if err != nil || got.NormalizedNeed == nil || !got.NormalizedNeed.Eligible || got.NormalizedNeed.NormalizedNeedID != r.NormalizedNeed.NormalizedNeedID {
		t.Fatalf("offline lock blocked or hid current result: %+v %v", got, err)
	}
	page, err := store.List(ctx, h.owner, 0, 20)
	if err != nil || len(page.Inputs) != 1 || page.Inputs[0].NormalizedNeed == nil {
		t.Fatal("offline lock blocked list", err)
	}
	raw := []byte(strings.Replace(original, "INTENT_ID", fmt.Sprint(h.intent), 1))
	if _, replayed, err := store.Create(ctx, h.owner, "online-independent", raw, 2); err != nil || !replayed {
		t.Fatal("offline lock blocked retry", err)
	}
	if _, _, err := store.Create(ctx, h.owner, "online-new-input", raw, 2); err != nil {
		t.Fatal("offline lock blocked new capture", err)
	}
	if err := tx.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	got, err = store.Get(ctx, h.owner, r.NeedInputID)
	if err != nil || !got.NormalizedNeed.Eligible {
		t.Fatal("failed offline publication lost baseline", err)
	}
}

func TestOfflinePublicationCoverageAndCAS(t *testing.T) {
	h := setup(t)
	r := createNormalized(t, h, "offline-coverage")
	store := need.Store{DB: h.db, IDs: h.ids}
	ctx := context.Background()
	baseID := r.NormalizedNeed.NormalizedNeedID
	partial := need.Vocabulary{Version: "v1", Needs: map[string]string{"PostgreSQL 索引": "database.index"}}
	p, err := store.Enrich(ctx, h.owner, r.NeedInputID, baseID, partial, 2)
	if err != nil || !p.Eligible || p.MappingStatus != need.MappingPartial {
		t.Fatalf("%+v %v", p, err)
	}
	replayed, err := store.Enrich(ctx, h.owner, r.NeedInputID, baseID, partial, 3)
	if err != nil || replayed.NormalizedNeedID != p.NormalizedNeedID {
		t.Fatal("enrichment retry duplicated", err)
	}
	changed := need.Vocabulary{Version: "v1", Needs: map[string]string{"PostgreSQL 索引": "different.id"}}
	if _, err := store.Enrich(ctx, h.owner, r.NeedInputID, p.NormalizedNeedID, changed, 4); !errors.Is(err, need.ErrProjectionConflict) {
		t.Fatal("mutable vocabulary version accepted", err)
	}
	full := need.Vocabulary{Version: "v2", Needs: map[string]string{"PostgreSQL 索引": "database.index", "数据库性能": "database.performance"}}
	if _, err := store.Enrich(ctx, h.owner, r.NeedInputID, baseID, full, 4); !errors.Is(err, need.ErrProjectionConflict) {
		t.Fatal("stale publisher overwrote current result", err)
	}
	p, err = store.Enrich(ctx, h.owner, r.NeedInputID, p.NormalizedNeedID, full, 4)
	if err != nil || !p.Eligible || p.MappingStatus != need.MappingMapped {
		t.Fatal(p, err)
	}
	got, err := store.Get(ctx, h.owner, r.NeedInputID)
	if err != nil || string(got.Input) != string(r.Input) || string(got.IntentSnapshot) != string(r.IntentSnapshot) {
		t.Fatal("source overwritten", err)
	}
	if _, err := store.Enrich(ctx, h.other, r.NeedInputID, p.NormalizedNeedID, full, 5); !errors.Is(err, need.ErrNotFound) {
		t.Fatal("cross-owner enrichment accepted", err)
	}
	if err := h.db.Exec("UPDATE agent_intent_actions SET version=2 WHERE intent_id=?", h.intent).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := store.Enrich(ctx, h.owner, r.NeedInputID, p.NormalizedNeedID, full, 5); !errors.Is(err, need.ErrStaleIntent) {
		t.Fatal("obsolete intent enriched", err)
	}
	got, err = store.Get(ctx, h.owner, r.NeedInputID)
	if err != nil || got.NormalizedNeed.Eligible {
		t.Fatal("obsolete intent remained eligible", err)
	}
	// Reconstruct versioned samples after the mutable Intent has advanced.
	var history []need.Projection
	if err := h.db.Where("need_input_id = ?", r.NeedInputID).Order("created_at").Find(&history).Error; err != nil {
		t.Fatal(err)
	}
	if len(history) != 3 {
		t.Fatalf("lost normalization history: %d revisions", len(history))
	}
	if string(got.Input) != string(r.Input) || string(got.IntentSnapshot) != string(r.IntentSnapshot) {
		t.Fatal("historical sample source changed with Intent")
	}
	for i, revision := range history {
		if revision.NeedInputID != r.NeedInputID || revision.AgentID != h.owner || revision.IntentID != h.intent || revision.IntentVersion != 1 {
			t.Fatal("historical revision lost exact source link", revision)
		}
		var n need.Normalized
		if err := json.Unmarshal(revision.Normalized, &n); err != nil {
			t.Fatal(err)
		}
		if len(n.CandidateNeeds) != 2 || len(n.MappedNeeds) != i || n.Desc != "请帮我记录这个需求：寻找 PostgreSQL 索引资料。 保留格式。" {
			t.Fatal("historical sample contents lost or overwritten", n)
		}
		if revision.TaxonomyVersion != []string{"", "v1", "v2"}[i] || revision.Status != []string{"superseded", "superseded", "active"}[i] {
			t.Fatal("historical sample metadata lost", revision)
		}
		if i > 0 && n.MappedNeeds["PostgreSQL 索引"] != "database.index" || i == 2 && n.MappedNeeds["数据库性能"] != "database.performance" {
			t.Fatal("historical mapping changed", n)
		}
	}
}

type failingIDs struct {
	source    *ids
	remaining int
}

func (f *failingIDs) NextID() (int64, error) {
	if f.remaining == 0 {
		return 0, errors.New("injected ID allocation failure")
	}
	f.remaining--
	return f.source.NextID()
}

func TestNormalizationAndEnrichmentAreAtomic(t *testing.T) {
	h := setup(t)
	raw := []byte(strings.Replace(original, "INTENT_ID", fmt.Sprint(h.intent), 1))
	store := need.Store{DB: h.db, IDs: &failingIDs{source: h.ids, remaining: 1}}
	if _, _, err := store.Create(context.Background(), h.owner, "atomic-create", raw, 1); err == nil {
		t.Fatal("injected failure ignored")
	}
	var count int64
	if err := h.db.Raw("SELECT count(*) FROM need_inputs WHERE agent_id=?", h.owner).Scan(&count).Error; err != nil || count != 0 {
		t.Fatal("partial create committed", count, err)
	}
	r := createNormalized(t, h, "atomic-enrichment")
	store.IDs = &failingIDs{source: h.ids}
	v := need.Vocabulary{Version: "v1", Needs: map[string]string{"PostgreSQL 索引": "database.index"}}
	if _, err := store.Enrich(context.Background(), h.owner, r.NeedInputID, r.NormalizedNeed.NormalizedNeedID, v, 2); err == nil {
		t.Fatal("injected publication failure ignored")
	}
	got, err := store.Get(context.Background(), h.owner, r.NeedInputID)
	if err != nil || got.NormalizedNeed == nil || !got.NormalizedNeed.Eligible || got.NormalizedNeed.NormalizedNeedID != r.NormalizedNeed.NormalizedNeedID {
		t.Fatal("failed enrichment lost baseline", err)
	}
	if _, err := store.Enrich(context.Background(), h.owner, r.NeedInputID, r.NormalizedNeed.NormalizedNeedID, need.Vocabulary{}, 2); err == nil {
		t.Fatal("invalid vocabulary accepted")
	}
}

func TestConcurrentOfflinePublishersAndOnlineRetries(t *testing.T) {
	h := setup(t)
	r := createNormalized(t, h, "parallel-normalize")
	store := need.Store{DB: h.db, IDs: h.ids}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, version := range []string{"v1", "v2"} {
		wg.Add(1)
		go func(version string) {
			defer wg.Done()
			_, err := store.Enrich(context.Background(), h.owner, r.NeedInputID, r.NormalizedNeed.NormalizedNeedID, need.Vocabulary{Version: version, Needs: map[string]string{"PostgreSQL 索引": "database.index"}}, 2)
			results <- err
		}(version)
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, need.ErrProjectionConflict) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatal("multiple publishers replaced same revision", successes)
	}
	got, replayed, err := store.Create(context.Background(), h.owner, "parallel-normalize", r.Input, 3)
	if err != nil || !replayed || got.NormalizedNeed.MappingStatus != need.MappingPartial {
		t.Fatal("online retry replaced enrichment", err)
	}
	var count int64
	if err := h.db.Raw("SELECT count(*) FROM normalized_needs WHERE need_input_id=? AND status='active'", r.NeedInputID).Scan(&count).Error; err != nil || count != 1 {
		t.Fatal(count, err)
	}
}

func TestPendingInputRetryGetsOnlineBaseline(t *testing.T) {
	h := setup(t)
	r := createNormalized(t, h, "legacy-pending")
	if err := h.db.Exec("DELETE FROM normalized_needs WHERE need_input_id=?", r.NeedInputID).Error; err != nil {
		t.Fatal(err)
	}
	if err := h.db.Exec("UPDATE need_inputs SET status='pending' WHERE need_input_id=?", r.NeedInputID).Error; err != nil {
		t.Fatal(err)
	}
	got, replayed, err := (need.Store{DB: h.db, IDs: h.ids}).Create(context.Background(), h.owner, "legacy-pending", r.Input, 2)
	if err != nil || !replayed || got.NeedInputID != r.NeedInputID || got.Status != "normalized" || got.NormalizedNeed == nil || !got.NormalizedNeed.Eligible {
		t.Fatalf("%+v %v", got, err)
	}
}
