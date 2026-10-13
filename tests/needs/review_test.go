package needs_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"eigenflux_server/pkg/need"
	"github.com/stretchr/testify/require"
)

func reviewInput(intent int64, version int, kind string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"schema_version":"need_input.v2","intent_id":"%d","intent_version":%d,"need_type":"%s","target":{"goal":"Find PostgreSQL expertise"}}`, intent, version, kind))
}
func reviewBody(t *testing.T, intent int64, version int, outcome string, inputs ...json.RawMessage) []byte {
	t.Helper()
	if inputs == nil {
		inputs = []json.RawMessage{}
	}
	raw, err := json.Marshal(need.CaptureReview{IntentID: intent, IntentVersion: int64(version), Outcome: outcome, Reason: "Reviewed confirmed Intent", Inputs: inputs})
	require.NoError(t, err)
	return raw
}

func TestCaptureReviewHTTPAndVersionLifecycle(t *testing.T) {
	h := setup(t)
	pending := func(token string) []any {
		return h.request(t, "GET", "/need-capture/pending?limit=2", token, "", "", 200)["intents"].([]any)
	}
	require.Len(t, pending(h.token), 1)
	require.Equal(t, fmt.Sprint(h.intent), pending(h.token)[0].(map[string]any)["intent_id"])
	h.request(t, "GET", "/need-capture/pending", "", "", "", 401)
	h.request(t, "GET", "/need-capture/pending?limit=11", h.token, "", "", 400)
	body := reviewBody(t, h.intent, 1, "captured", reviewInput(h.intent, 1, "broadcast"), reviewInput(h.intent, 1, "agent"))
	h.request(t, "POST", "/need-capture/complete", h.otherToken, "", string(body), 409)
	got := h.request(t, "POST", "/need-capture/complete", h.token, "", string(body), 200)
	require.Equal(t, false, got["replayed"])
	require.Empty(t, pending(h.token))
	require.Len(t, pending(h.otherToken), 1)
	require.Equal(t, true, h.request(t, "POST", "/need-capture/complete", h.token, "", string(body), 200)["replayed"])
	var count int64
	require.NoError(t, h.db.Table("need_inputs").Where("agent_id=?", h.owner).Count(&count).Error)
	require.EqualValues(t, 2, count)
	h.request(t, "POST", "/need-capture/complete", h.token, "", string(reviewBody(t, h.intent, 1, "no_need")), 409)
	require.NoError(t, h.db.Exec("UPDATE agent_intent_actions SET version=2 WHERE intent_id=?", h.intent).Error)
	rows := pending(h.token)
	require.Len(t, rows, 1)
	require.Empty(t, rows[0].(map[string]any)["existing_inputs"])
	h.request(t, "POST", "/need-capture/complete", h.token, "", string(body), 409)
	body = reviewBody(t, h.intent, 2, "no_need")
	h.request(t, "POST", "/need-capture/complete", h.token, "", string(body), 200)
	require.Empty(t, pending(h.token))
	require.NoError(t, h.db.Exec("UPDATE agent_intent_actions SET version=3,status='deleted' WHERE intent_id=?", h.intent).Error)
	require.Empty(t, pending(h.token))
	h.request(t, "POST", "/need-capture/complete", h.token, "", string(reviewBody(t, h.intent, 3, "no_need")), 409)
}

type failSecondID struct {
	count int
	next  *ids
}

func (f *failSecondID) NextID() (int64, error) {
	f.count++
	if f.count == 2 {
		return 0, fmt.Errorf("second capture failed")
	}
	return f.next.NextID()
}

func TestCaptureReviewRollbackRetryAndExistingKinds(t *testing.T) {
	h := setup(t)
	ctx := context.Background()
	store := need.Store{DB: h.db, IDs: &failSecondID{next: h.ids}}
	raw := reviewBody(t, h.intent, 1, "captured", reviewInput(h.intent, 1, "broadcast"), reviewInput(h.intent, 1, "agent"))
	_, err := store.CompleteCapture(ctx, h.owner, raw, 1)
	require.Error(t, err)
	var count int64
	require.NoError(t, h.db.Table("need_inputs").Where("agent_id=?", h.owner).Count(&count).Error)
	require.Zero(t, count)
	p, err := store.PendingCaptures(ctx, h.owner, 2)
	require.NoError(t, err)
	require.Len(t, p.Intents, 1)
	store.IDs = h.ids
	_, _, err = store.Create(ctx, h.owner, "existing-capture", reviewInput(h.intent, 1, "broadcast"), 1)
	require.NoError(t, err)
	_, err = store.CompleteCapture(ctx, h.owner, raw, 1)
	require.Error(t, err, "must not duplicate a concurrent direct capture")
	_, err = store.CompleteCapture(ctx, h.owner, reviewBody(t, h.intent, 1, "no_need"), 1)
	require.Error(t, err)
	p, err = store.PendingCaptures(ctx, h.owner, 2)
	require.NoError(t, err)
	require.Contains(t, string(p.Intents[0].ExistingInputs), "broadcast")
	raw = reviewBody(t, h.intent, 1, "captured", reviewInput(h.intent, 1, "agent"))
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	replays := make(chan bool, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			replay, err := store.CompleteCapture(ctx, h.owner, raw, 2)
			errs <- err
			replays <- replay
		}()
	}
	wg.Wait()
	close(errs)
	close(replays)
	for err := range errs {
		require.NoError(t, err)
	}
	repeated := 0
	for r := range replays {
		if r {
			repeated++
		}
	}
	require.Equal(t, 3, repeated)
	require.NoError(t, h.db.Table("need_inputs").Where("agent_id=?", h.owner).Count(&count).Error)
	require.EqualValues(t, 2, count)
}

func TestCapturePendingBoundAndExistingOnlyCompletion(t *testing.T) {
	h := setup(t)
	store := need.Store{DB: h.db, IDs: h.ids}
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		require.NoError(t, h.db.Exec(`INSERT INTO agent_intent_actions(agent_id,watch_for,trigger_when,action_instruction,action_policy,priority,source,status,version,created_at,updated_at) VALUES (?,'Find SQL','Useful','summarize','analyze_only',5,'human_edit','active',1,1,1)`, h.owner).Error)
	}
	p, err := store.PendingCaptures(ctx, h.owner, 2)
	require.NoError(t, err)
	require.Len(t, p.Intents, 2)
	require.True(t, p.HasMore)
	_, err = store.CompleteCapture(ctx, h.owner, reviewBody(t, h.intent, 1, "captured"), 1)
	require.Error(t, err)
	_, _, err = store.Create(ctx, h.owner, "existing-only", reviewInput(h.intent, 1, "agent"), 1)
	require.NoError(t, err)
	_, err = store.CompleteCapture(ctx, h.owner, reviewBody(t, h.intent, 1, "captured"), 2)
	require.NoError(t, err)
	p, err = store.PendingCaptures(ctx, h.owner, 10)
	require.NoError(t, err)
	require.Len(t, p.Intents, 3)
	require.False(t, p.HasMore)
}

func TestActiveNeedsCoverKindsBeforeBound(t *testing.T) {
	h := setup(t)
	store := need.Store{DB: h.db, IDs: h.ids}
	ctx := context.Background()
	for i := 0; i < 8; i++ {
		_, _, err := store.Create(ctx, h.owner, fmt.Sprintf("many-broadcast-%d", i), reviewInput(h.intent, 1, "broadcast"), int64(20+i))
		require.NoError(t, err)
	}
	for _, kind := range []string{"agent", "commission"} {
		_, _, err := store.Create(ctx, h.owner, "one-"+kind, reviewInput(h.intent, 1, kind), 1)
		require.NoError(t, err)
	}
	active, err := store.Active(ctx, h.owner, []string{"broadcast", "agent", "commission"}, 100)
	require.NoError(t, err)
	require.Len(t, active, 5)
	kinds := map[string]bool{}
	for _, n := range active {
		in, err := n.ExecutionInput()
		require.NoError(t, err)
		kinds[in.NeedType] = true
	}
	require.Len(t, kinds, 3)
}
