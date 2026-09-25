package needs_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"eigenflux_server/pkg/need"
)

func TestDiscoveryCurrentNeedReader(t *testing.T) {
	h := setup(t)
	ctx := context.Background()
	store := need.Store{DB: h.db, IDs: h.ids}
	first := createNormalized(t, h, "reader-first")
	got, err := store.Current(ctx, h.owner, first.NeedInputID)
	if err != nil || got.ProjectionID != first.NormalizedNeed.NormalizedNeedID || got.InputID != first.NeedInputID || got.MappingStatus != need.MappingUnmapped {
		t.Fatal(got, err)
	}
	for _, owner := range []int64{h.other, 0} {
		if _, err = store.Current(ctx, owner, first.NeedInputID); !errors.Is(err, need.ErrNotFound) {
			t.Fatal("ownership", err)
		}
	}
	if err = store.CheckIntent(ctx, h.other, h.intent, 1); !errors.Is(err, need.ErrStaleIntent) {
		t.Fatal("inline owner boundary", err)
	}
	if err = store.CheckIntent(ctx, h.owner, h.intent, 2); !errors.Is(err, need.ErrStaleIntent) {
		t.Fatal("inline version boundary", err)
	}
	// A slow offline writer cannot block a reader or hide its committed snapshot.
	tx := h.db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	if err = tx.Exec("UPDATE normalized_needs SET status='superseded' WHERE need_input_id=?", first.NeedInputID).Error; err != nil {
		t.Fatal(err)
	}
	got, err = store.Current(ctx, h.owner, first.NeedInputID)
	if err != nil || got.ProjectionID != first.NormalizedNeed.NormalizedNeedID {
		t.Fatal(got, err)
	}
	if err = tx.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	p, err := store.Enrich(ctx, h.owner, first.NeedInputID, got.ProjectionID, need.Vocabulary{Version: "reader-v1", Needs: map[string]string{"PostgreSQL 索引": "database.index"}}, 2)
	if err != nil {
		t.Fatal(err)
	}
	got, err = store.Current(ctx, h.owner, first.NeedInputID)
	if err != nil || got.ProjectionID != p.NormalizedNeedID || got.TaxonomyVersion != "reader-v1" || got.MappingStatus != need.MappingPartial {
		t.Fatal(got, err)
	}
	for _, mutate := range []string{
		"UPDATE agent_intent_actions SET version=2 WHERE intent_id=?",
		"UPDATE agent_intent_actions SET version=1,status='deleted' WHERE intent_id=?",
	} {
		if err = h.db.Exec(mutate, h.intent).Error; err != nil {
			t.Fatal(err)
		}
		if _, err = store.Current(ctx, h.owner, first.NeedInputID); !errors.Is(err, need.ErrStaleIntent) {
			t.Fatal("inactive accepted", err)
		}
		active, err := store.Active(ctx, h.owner, []string{"broadcast"}, 100)
		if err != nil || len(active) != 0 {
			t.Fatal(active, err)
		}
	}
}

func TestDiscoveryActiveNeedSelection(t *testing.T) {
	h := setup(t)
	ctx := context.Background()
	store := need.Store{DB: h.db, IDs: h.ids}
	base, err := need.Decode([]byte(strings.Replace(original, "INTENT_ID", fmt.Sprint(h.intent), 1)))
	if err != nil {
		t.Fatal(err)
	}
	create := func(key, kind string, priority *float64, deadline *int64, now int64) need.Record {
		t.Helper()
		in := base
		in.NeedType = kind
		in.Priority = priority
		in.Constraints.DeadlineMS = deadline
		raw, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		r, _, err := store.Create(ctx, h.owner, key, raw, now)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	high := 1.
	zero := 0.
	past := int64(99)
	create("reader-expired", "broadcast", &high, &past, 1000)
	create("reader-other-kind", "commission", &high, nil, 1000)
	older := create("reader-older", "broadcast", &high, nil, 10)
	newer := create("reader-newer", "broadcast", &high, nil, 11)
	for i := 0; i < 6; i++ {
		create(fmt.Sprintf("reader-low-%d", i), "broadcast", &zero, nil, int64(i))
	}
	active, err := store.Active(ctx, h.owner, []string{"broadcast"}, 100)
	if err != nil || len(active) != 5 || active[0].InputID != newer.NeedInputID || active[1].InputID != older.NeedInputID {
		t.Fatal(active, err)
	}
	for _, n := range active {
		if n.Input.NeedType != "broadcast" || n.MappingStatus != need.MappingUnmapped {
			t.Fatal(n)
		}
	}
	foreign, err := store.Active(ctx, h.other, []string{"broadcast"}, 100)
	if err != nil || len(foreign) != 0 {
		t.Fatal(foreign, err)
	}
}
