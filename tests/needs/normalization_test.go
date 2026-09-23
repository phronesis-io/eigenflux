package needs_test

import (
	"context"
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
	partial := need.Vocabulary{Version: "v1", Intents: map[string]string{"PostgreSQL 索引": "database.index"}}
	p, err := store.Enrich(ctx, h.owner, r.NeedInputID, baseID, partial, 2)
	if err != nil || !p.Eligible || p.MappingStatus != need.MappingPartial {
		t.Fatalf("%+v %v", p, err)
	}
	replayed, err := store.Enrich(ctx, h.owner, r.NeedInputID, baseID, partial, 3)
	if err != nil || replayed.NormalizedNeedID != p.NormalizedNeedID {
		t.Fatal("enrichment retry duplicated", err)
	}
	changed := need.Vocabulary{Version: "v1", Intents: map[string]string{"PostgreSQL 索引": "different.id"}}
	if _, err := store.Enrich(ctx, h.owner, r.NeedInputID, p.NormalizedNeedID, changed, 4); !errors.Is(err, need.ErrProjectionConflict) {
		t.Fatal("mutable vocabulary version accepted", err)
	}
	full := need.Vocabulary{Version: "v2", Intents: map[string]string{"PostgreSQL 索引": "database.index", "数据库性能": "database.performance"}}
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
	v := need.Vocabulary{Version: "v1", Intents: map[string]string{"PostgreSQL 索引": "database.index"}}
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
			_, err := store.Enrich(context.Background(), h.owner, r.NeedInputID, r.NormalizedNeed.NormalizedNeedID, need.Vocabulary{Version: version, Intents: map[string]string{"PostgreSQL 索引": "database.index"}}, 2)
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
