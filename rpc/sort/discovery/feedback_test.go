package discovery

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"eigenflux_server/rpc/sort/discoverylr"
)

func TestFeedbackBatchRanksAndFallsBackAtomically(t *testing.T) {
	e, _, _, _ := challengerFixture()
	var calls atomic.Int32
	var mode atomic.Value
	mode.Store("valid")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var batch feedbackBatch
		if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
			t.Error(err)
			return
		}
		if batch.Owner != 1 || batch.When != testNow || len(batch.Candidates) < 3 {
			t.Errorf("unexpected batch: %+v", batch)
		}
		rows := []map[string]any{}
		for _, c := range batch.Candidates {
			if len(c.Features) != len(discoverylr.RatingFeatureNames) || c.Channels == nil {
				t.Error("expected frozen v1 inputs")
			}
			// Make item 2 win even though v2 assigns a different ordering.
			grade := -1.0
			if c.Item == 2 {
				grade = 2
			}
			rows = append(rows, map[string]any{"score": grade, "item_cold": false})
		}
		// Independent local-midnight computation.
		zone := time.FixedZone("Shanghai", 8*3600)
		when := time.UnixMilli(batch.When).In(zone)
		cutoff := time.Date(when.Year(), when.Month(), when.Day(), 0, 0, 0, 0, zone).UnixMilli()
		switch mode.Load() {
		case "missing":
			delete(rows[len(rows)-1], "item_cold")
		case "range":
			rows[len(rows)-1]["score"] = 2.01
		case "day":
			cutoff -= 86400000
		case "short":
			rows = rows[:1]
		case "unavailable":
			w.WriteHeader(503)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"feature_contract_version": feedbackContract, "model_version": "test-dcn", "snapshot_cutoff_ms": cutoff, "rows": rows})
	}))
	defer server.Close()
	e.Feedback = &FeedbackClient{server.URL, server.Client()}
	runProfile(t, e)
	if calls.Load() != 0 {
		t.Fatal("disabled scorer received work")
	}
	e.FeedbackBPS = 10000
	x := runProfile(t, e)
	if calls.Load() != 1 || x.Candidates[0].Document.Ref.ID != 2 {
		t.Fatalf("unexpected order %v, calls %d", ids(x), calls.Load())
	}
	for _, c := range x.Candidates {
		if c.LR != nil || c.Feedback == nil || c.Feedback.Baseline.Contract != discoverylr.RatingV2Contract || c.Feedback.ItemCold || PublicItem(c).Match["score_kind"] != "expected_feedbackscore" || c.RankingScore() != (c.Feedback.Score+1)/3 {
			t.Fatalf("incorrect model evidence: %+v", c)
		}
	}
	for _, bad := range []string{"short", "missing", "range", "day", "unavailable"} {
		mode.Store(bad)
		for _, c := range runProfile(t, e).Candidates {
			if c.Feedback != nil || c.LR == nil || c.LR.Contract != discoverylr.RatingV2Contract {
				t.Fatalf("%s: mixed ranking", bad)
			}
		}
	}
}

func TestFeedbackOutcomeKeepsAssignmentAcrossFallback(t *testing.T) {
	e := Engine{RatingNextBPS: 10000}
	v1 := []Candidate{{LR: &discoverylr.Result{Contract: discoverylr.RatingContract}}}
	for _, arm := range []string{"control", "treatment"} {
		if got := e.feedbackOutcome(arm, 1, v1, nil); got != "fallback" {
			t.Fatal(arm, got)
		}
		if got := e.feedbackOutcome(arm, 1, []Candidate{{LR: &discoverylr.Result{Contract: discoverylr.SnapshotContract}}}, nil); got != "gbdt_bypass" {
			t.Fatal(arm, got)
		}
		if got := e.feedbackOutcome(arm, 1, nil, nil); got != "empty" {
			t.Fatal(arm, got)
		}
	}
	e.RatingNextBPS = 0
	if e.feedbackOutcome("control", 1, v1, nil) != "served" || e.feedbackOutcome("treatment", 1, []Candidate{{Feedback: &FeedbackEvidence{}}}, nil) != "served" {
		t.Fatal("incorrect served classification")
	}
}
