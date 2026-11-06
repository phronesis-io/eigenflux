package needs_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"eigenflux_server/pkg/need"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func reviewAtRevision(t *testing.T, intent, revision int64, outcome string, inputs ...json.RawMessage) []byte {
	t.Helper()
	raw := reviewBody(t, intent, 1, outcome, inputs...)
	var r need.CaptureReview
	require.NoError(t, json.Unmarshal(raw, &r))
	r.CaptureRevision = revision
	raw, err := json.Marshal(r)
	require.NoError(t, err)
	return raw
}

func TestSelectedNoNeedReopenLifecycle(t *testing.T) {
	h := setup(t)
	statePath := fmt.Sprintf("/need-capture/review?intent_id=%d&intent_version=1", h.intent)
	reopen := fmt.Sprintf(`{"intent_id":"%d","intent_version":1,"expected_capture_revision":0}`, h.intent)
	h.request(t, "GET", statePath, "", "", "", 401)
	h.request(t, "GET", statePath, h.otherToken, "", "", 409)
	h.request(t, "GET", "/need-capture/review?intent_id=01&intent_version=1", h.token, "", "", 400)
	require.Equal(t, "pending", h.request(t, "GET", statePath, h.token, "", "", 200)["state"])
	h.request(t, "POST", "/need-capture/reopen", h.token, "", reopen, 409)
	original := reviewBody(t, h.intent, 1, "no_need")
	h.request(t, "POST", "/need-capture/complete", h.token, "", string(original), 200)
	require.Equal(t, "no_need", h.request(t, "GET", statePath, h.token, "", "", 200)["state"])
	h.request(t, "POST", "/need-capture/reopen", h.otherToken, "", reopen, 409)
	var before string
	require.NoError(t, h.db.Raw("SELECT row_to_json(a)::text FROM agent_intent_actions a WHERE intent_id=?", h.intent).Scan(&before).Error)
	out := h.request(t, "POST", "/need-capture/reopen", h.token, "", reopen, 200)
	require.Equal(t, false, out["replayed"])
	review := out["review"].(map[string]any)
	require.Equal(t, "pending", review["state"])
	require.Equal(t, float64(1), review["capture_revision"])
	rows := h.request(t, "GET", "/need-capture/pending?limit=2", h.token, "", "", 200)["intents"].([]any)
	require.Len(t, rows, 1)
	require.Equal(t, float64(1), rows[0].(map[string]any)["capture_revision"])
	late := h.request(t, "POST", "/need-capture/complete", h.token, "", string(original), 409)
	require.Equal(t, "NEED_CAPTURE_REVISION_STALE", late["error"].(map[string]any)["code"])
	h.request(t, "POST", "/need-capture/complete", h.token, "", string(reviewAtRevision(t, h.intent, 1, "no_need")), 200)
	// A retry of the original reopen does not undo the new legitimate no_need.
	retry := h.request(t, "POST", "/need-capture/reopen", h.token, "", reopen, 200)
	require.Equal(t, true, retry["replayed"])
	require.Equal(t, "no_need", retry["review"].(map[string]any)["state"])
	require.Empty(t, h.request(t, "GET", "/need-capture/pending", h.token, "", "", 200)["intents"])
	second := fmt.Sprintf(`{"intent_id":"%d","intent_version":1,"expected_capture_revision":1}`, h.intent)
	h.request(t, "POST", "/need-capture/reopen", h.token, "", second, 200)
	h.request(t, "POST", "/need-capture/reopen", h.token, "", reopen, 409)
	captured := reviewAtRevision(t, h.intent, 2, "captured", reviewInput(h.intent, 1, "broadcast"))
	h.request(t, "POST", "/need-capture/complete", h.token, "", string(captured), 200)
	require.Equal(t, true, h.request(t, "POST", "/need-capture/complete", h.token, "", string(captured), 200)["replayed"])
	require.Equal(t, "captured", h.request(t, "GET", statePath, h.token, "", "", 200)["state"])
	third := fmt.Sprintf(`{"intent_id":"%d","intent_version":1,"expected_capture_revision":2}`, h.intent)
	h.request(t, "POST", "/need-capture/reopen", h.token, "", third, 409)
	var after string
	require.NoError(t, h.db.Raw("SELECT row_to_json(a)::text FROM agent_intent_actions a WHERE intent_id=?", h.intent).Scan(&after).Error)
	require.Equal(t, before, after, "reopening must not edit the source Intent")
	var count int64
	require.NoError(t, h.db.Table("need_inputs").Where("agent_id=?", h.owner).Count(&count).Error)
	require.EqualValues(t, 1, count)
	require.NoError(t, h.db.Exec("UPDATE agent_intent_actions SET version=2 WHERE intent_id=?", h.intent).Error)
	h.request(t, "GET", statePath, h.token, "", "", 409)
	h.request(t, "POST", "/need-capture/reopen", h.token, "", second, 409)
}

func TestConcurrentReopenAndLateCompletionCannotErasePending(t *testing.T) {
	h := setup(t)
	ctx := context.Background()
	store := need.Store{DB: h.db, IDs: h.ids}
	original := reviewBody(t, h.intent, 1, "no_need")
	_, err := store.CompleteCapture(ctx, h.owner, original, 1)
	require.NoError(t, err)
	revision := int64(0)
	request := need.ReopenCaptureRequest{IntentID: h.intent, IntentVersion: 1, ExpectedCaptureRevision: &revision}
	var wg sync.WaitGroup
	errs := make(chan error, 5)
	replays := make(chan bool, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, replayed, err := store.ReopenCapture(ctx, h.owner, request, 2)
			errs <- err
			replays <- replayed
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err := store.CompleteCapture(ctx, h.owner, original, 3)
		if errors.Is(err, need.ErrStaleCapture) {
			err = nil
		}
		errs <- err
	}()
	wg.Wait()
	close(errs)
	close(replays)
	for err := range errs {
		require.NoError(t, err)
	}
	replayed := 0
	for r := range replays {
		if r {
			replayed++
		}
	}
	require.Equal(t, 3, replayed)
	state, err := store.CaptureReviewState(ctx, h.owner, h.intent, 1)
	require.NoError(t, err)
	require.Equal(t, "pending", state.State)
	require.EqualValues(t, 1, state.CaptureRevision)
	var reason string
	require.NoError(t, h.db.Raw("SELECT reason FROM need_capture_reviews WHERE agent_id=? AND intent_id=?", h.owner, h.intent).Scan(&reason).Error)
	require.Equal(t, "Reviewed confirmed Intent", reason, "retain the previous decision until re-review succeeds")
	_, err = store.CompleteCapture(ctx, h.owner, reviewAtRevision(t, h.intent, 1, "captured"), 4)
	require.Error(t, err)
	state, err = store.CaptureReviewState(ctx, h.owner, h.intent, 1)
	require.NoError(t, err)
	require.Equal(t, "pending", state.State)
}

func TestSelectedReopenEnforcesAuthorizationAndEnrollmentBound(t *testing.T) {
	h := setup(t)
	body := fmt.Sprintf(`{"intent_id":"%d","intent_version":1,"expected_capture_revision":0}`, h.intent)
	h.request(t, "POST", "/need-capture/reopen", "", "", body, 401)
	h.request(t, "POST", "/need-capture/complete", h.token, "", string(reviewBody(t, h.intent, 1, "no_need")), 200)
	require.NoError(t, h.db.Exec("UPDATE agent_credential_sessions SET scopes=ARRAY['context:read'] WHERE principal_id=?", h.owner).Error)
	h.request(t, "POST", "/need-capture/reopen", h.token, "", body, 403)
	require.NoError(t, h.db.Exec("UPDATE agent_credential_sessions SET scopes=ARRAY['context:read','context:write'] WHERE principal_id=?", h.owner).Error)
	require.NoError(t, h.db.Exec("UPDATE agent_onboarding_v2 SET state='in_progress',completed_at=NULL WHERE agent_id=?", h.owner).Error)
	h.request(t, "POST", "/need-capture/reopen", h.token, "", body, 409)
	require.NoError(t, h.db.Exec("UPDATE agent_onboarding_v2 SET state='completed',completed_at=1 WHERE agent_id=?", h.owner).Error)
	h.request(t, "POST", "/need-capture/reopen", h.token, "", body, 200)
	// Explicit selection does not bypass the usual priority order or two-item cap.
	for i := 0; i < 3; i++ {
		require.NoError(t, h.db.Exec(`INSERT INTO agent_intent_actions(agent_id,watch_for,trigger_when,action_instruction,action_policy,priority,source,status,version,created_at,updated_at) VALUES (?,'Find SQL','Useful','summarize','analyze_only',20,'human_edit','active',1,1,1)`, h.owner).Error)
	}
	pending := h.request(t, "GET", "/need-capture/pending?limit=2", h.token, "", "", 200)
	require.Len(t, pending["intents"], 2)
	require.Equal(t, true, pending["has_more"])
	for _, item := range pending["intents"].([]any) {
		require.NotEqual(t, fmt.Sprint(h.intent), item.(map[string]any)["intent_id"])
	}
}

func TestCaptureReopenMigrationPreservesRowsAndFence(t *testing.T) {
	h := setup(t)
	raw, err := os.ReadFile("../../migrations/000132_need_capture_reopen.sql")
	require.NoError(t, err)
	up, down, ok := strings.Cut(string(raw), "-- +goose Down")
	require.True(t, ok)
	rollback := errors.New("rollback isolated migration fixture")
	err = h.db.Transaction(func(tx *gorm.DB) error {
		schema := fmt.Sprintf("need_reopen_migration_%d", h.owner)
		require.NoError(t, tx.Exec("CREATE SCHEMA "+schema).Error)
		require.NoError(t, tx.Exec("SET LOCAL search_path TO "+schema).Error)
		require.NoError(t, tx.Exec("CREATE TABLE need_capture_reviews (outcome TEXT NOT NULL); INSERT INTO need_capture_reviews VALUES ('captured'),('no_need')").Error)
		require.NoError(t, tx.Exec(up).Error)
		var count int64
		require.NoError(t, tx.Raw("SELECT count(*) FROM need_capture_reviews WHERE capture_revision=0 AND reopened_at IS NULL").Scan(&count).Error)
		require.EqualValues(t, 2, count, "migration must not reopen existing outcomes")
		require.NoError(t, tx.Exec(down).Error)
		require.NoError(t, tx.Exec(up).Error)
		require.NoError(t, tx.Exec("UPDATE need_capture_reviews SET capture_revision=1,reopened_at=1 WHERE outcome='no_need'").Error)
		require.NoError(t, tx.Exec("SAVEPOINT before_down").Error)
		require.ErrorContains(t, tx.Exec(down).Error, "preserve stale-request fencing")
		require.NoError(t, tx.Exec("ROLLBACK TO SAVEPOINT before_down").Error)
		require.NoError(t, tx.Raw("SELECT count(*) FROM need_capture_reviews WHERE capture_revision=1 AND reopened_at=1").Scan(&count).Error)
		require.EqualValues(t, 1, count)
		return rollback
	})
	require.ErrorIs(t, err, rollback)
}
