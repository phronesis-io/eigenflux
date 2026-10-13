package needs_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"eigenflux_server/pkg/need"
)

func TestDirectNeedCaptureAndHistoricalReplay(t *testing.T) {
	h := setup(t)
	ctx := context.Background()
	store := need.Store{DB: h.db, IDs: h.ids}
	raw := fmt.Sprintf(`{"schema_version":"need_input.v2","intent_id":"%d","intent_version":1,"need_type":"commission","target":{"goal":"Diagnose PostgreSQL slow queries","context":"  Report first.\nKeep context. "},"requirements":[{"text":"Do not upload production data","source_quote":"no uploads"}],"preferences":[{"text":"Chinese preferred"}],"constraints":{"budget_max_fen":50000,"currency":"CNY"}}`, h.intent)
	created := h.request(t, "POST", "/need-inputs", h.token, "direct-v2-input", raw, 201)["need_input"].(map[string]any)
	if created["status"] != "active" || created["eligible"] != true || created["normalized_need"] != nil {
		t.Fatal(created)
	}
	page, err := store.List(ctx, h.owner, 0, 20)
	if err != nil || len(page.Inputs) != 1 {
		t.Fatal(page, err)
	}
	r := page.Inputs[0]
	var submitted, stored any
	if err = json.Unmarshal([]byte(raw), &submitted); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(r.Input, &stored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(submitted, stored) {
		t.Fatal("input conditions or source text changed")
	}
	var count int64
	if err = h.db.Raw("SELECT count(*) FROM normalized_needs WHERE need_input_id=?", r.NeedInputID).Scan(&count).Error; err != nil || count != 0 {
		t.Fatal("new projection created", count, err)
	}
	if err = h.db.Exec("UPDATE agent_intent_actions SET version=2 WHERE intent_id=?", h.intent).Error; err != nil {
		t.Fatal(err)
	}
	got, replayed, err := store.Create(ctx, h.owner, "direct-v2-input", []byte(raw), 200)
	if err != nil || !replayed || got.Eligible || string(got.Input) != string(r.Input) || string(got.IntentSnapshot) != string(r.IntentSnapshot) {
		t.Fatal("history changed", got, err)
	}

}

func TestLegacyNeedReadsDoNotDependOnProjection(t *testing.T) {
	h := setup(t)
	ctx := context.Background()
	store := need.Store{DB: h.db, IDs: h.ids}
	raw := []byte(strings.Replace(original, "INTENT_ID", fmt.Sprint(h.intent), 1))
	r, _, err := store.Create(ctx, h.owner, "legacy-execution", raw, 1)
	if err != nil {
		t.Fatal(err)
	}
	// A pre-migration normalized input remains usable even without a projection.
	if err = h.db.Exec("UPDATE need_inputs SET status='normalized' WHERE need_input_id=?", r.NeedInputID).Error; err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, h.owner, r.NeedInputID)
	if err != nil || !got.Eligible {
		t.Fatal(got, err)
	}
	for _, status := range []string{"pending", "failed", "superseded"} {
		if err = h.db.Exec("UPDATE need_inputs SET status=? WHERE need_input_id=?", status, r.NeedInputID).Error; err != nil {
			t.Fatal(err)
		}
		got, replayed, err := store.Create(ctx, h.owner, "legacy-execution", raw, 2)
		if err != nil || !replayed || got.Eligible || got.Status != status {
			t.Fatal("retry changed lifecycle", got, err)
		}
	}
}

type failingIDs struct{}

func (failingIDs) NextID() (int64, error) { return 0, errors.New("injected ID allocation failure") }
func TestNeedInputFailureDoesNotCommit(t *testing.T) {
	h := setup(t)
	raw := []byte(strings.Replace(original, "INTENT_ID", fmt.Sprint(h.intent), 1))
	_, _, err := (need.Store{DB: h.db, IDs: failingIDs{}}).Create(context.Background(), h.owner, "failed-capture", raw, 1)
	if err == nil {
		t.Fatal("ID failure ignored")
	}
	var count int64
	if err = h.db.Raw("SELECT count(*) FROM need_inputs WHERE agent_id=?", h.owner).Scan(&count).Error; err != nil || count != 0 {
		t.Fatal("partial capture committed", count, err)
	}
}
