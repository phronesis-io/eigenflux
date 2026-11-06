package followup

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"fluxspace.ai/hub/models"
	"fluxspace.ai/hub/services"
)

func finishedView() models.SessionView {
	return models.SessionView{
		SessionID: "sess_1", InvitationID: "inv_1",
		Status: models.SessionFinished, Outcome: "partial",
		Round: 5, TotalRounds: 5,
		Seats: []models.Seat{
			{SeatClaim: models.SeatClaim{SeatID: "seat_0", OwnerID: "o0", AgentID: "a0", CompanionID: "c0", DisplayName: "Lin Shen"}},
			{SeatClaim: models.SeatClaim{SeatID: "seat_1", OwnerID: "o1", AgentID: "a1", CompanionID: "c1", DisplayName: "Su Wan"}},
			{SeatClaim: models.SeatClaim{SeatID: "seat_2", OwnerID: "official2", AgentID: "oa2", CompanionID: "oc2", Official: true}},
			{SeatClaim: models.SeatClaim{SeatID: "seat_3", OwnerID: "official3", AgentID: "oa3", CompanionID: "oc3", Official: true}},
		},
		Metrics: map[string]models.Metric{
			"truth": {Key: "truth", Label: "Truth", Value: 62, Max: 80},
		},
	}
}

func event(id int64, kind, seat, visibility, payload string, round int) models.Event {
	return models.Event{
		ID: id, Type: kind, SessionID: "sess_1", Round: round,
		SeatID: seat, Visibility: visibility,
		Payload: json.RawMessage(payload), TS: 1730000000000 + id,
	}
}

func events() []models.Event {
	return []models.Event{
		event(1, models.EventDecision, "seat_0", models.VisibilityPublic,
			`{"action_id":"act_verify","summaries":{"one_liner":"Lin Shen chose verify the ledger"}}`, 1),
		event(2, models.EventDiscussion, "seat_0", models.VisibilityPublic,
			`{"text":"I found the ledger"}`, 1),
		event(3, models.EventDiscussion, "seat_1", models.VisibilityPrivate,
			`{"text":"private secret must never leak"}`, 1),
		event(4, models.EventBranchEvent, "", models.VisibilityPublic,
			`{"text":"The archive lights went out"}`, 2),
		event(5, models.EventMatchEnd, "", models.VisibilityPublic,
			`{"outcome":"partial","round":5}`, 5),
	}
}

func TestBuildRequiresFinishedSessionWithOutcome(t *testing.T) {
	view := finishedView()
	view.Status = models.SessionLive
	if _, err := Build(view, events(), 1); err == nil || !strings.Contains(err.Error(), "SESSION_NOT_FINISHED") {
		t.Fatalf("got %v", err)
	}
	view = finishedView()
	view.Outcome = ""
	if _, err := Build(view, events(), 1); err == nil || !strings.Contains(err.Error(), "OUTCOME_MISSING") {
		t.Fatalf("got %v", err)
	}
}

func TestBuildProducesSourceTaggedTruthfulMemories(t *testing.T) {
	view := finishedView()
	evs := events()
	doc, err := Build(view, evs, 1730000009999)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if doc.Kind != models.FollowUpKind || doc.Outcome != "partial" || doc.Rounds != 5 {
		t.Fatalf("doc = %+v", doc)
	}
	if len(doc.Memories) == 0 {
		t.Fatal("no memories derived from the event log")
	}
	known := make(map[int64]models.Event, len(evs))
	for _, ev := range evs {
		known[ev.ID] = ev
	}
	perCompanion := map[string]int{}
	for _, mem := range doc.Memories {
		parts := strings.SplitN(mem.Source.RefID, ":", 2)
		if len(parts) != 2 {
			t.Fatalf("source ref %q is not machine-readable", mem.Source.RefID)
		}
		id, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			t.Fatalf("source ref %q is not traceable: %v", mem.Source.RefID, err)
		}
		source, ok := known[id]
		if !ok {
			t.Fatalf("memory %s points at a nonexistent event %d", mem.ID, id)
		}
		if mem.Source.Kind != source.Type || mem.Source.Round != source.Round {
			t.Fatalf("memory source %+v does not match event %+v", mem.Source, source)
		}
		if !strings.HasPrefix(mem.Content, "Round ") && !strings.HasPrefix(mem.Content, "Match ended") {
			t.Fatalf("content is not derived from recorded facts: %q", mem.Content)
		}
		if strings.Contains(mem.Content, "private secret") {
			t.Fatal("a private channel leaked into the follow-up")
		}
		if mem.CompanionID != "c0" && mem.CompanionID != "c1" {
			t.Fatalf("official backfill should not own memories: %s", mem.CompanionID)
		}
		perCompanion[mem.CompanionID]++
	}
	for id, n := range perCompanion {
		if n > MaxMemoriesPerCompanion {
			t.Fatalf("companion %s has %d memories, cap is %d", id, n, MaxMemoriesPerCompanion)
		}
	}
	if len(doc.RelationshipUpdates) == 0 {
		t.Fatal("public discussion should produce a directional draft")
	}
	for _, u := range doc.RelationshipUpdates {
		if !u.Draft || u.SourceEventID == 0 || u.Direction != "toward" {
			t.Fatalf("relationship draft = %+v", u)
		}
	}
	if len(doc.UnresolvedThreads) == 0 || !strings.Contains(doc.UnresolvedThreads[0], "62/80") {
		t.Fatalf("threads = %v", doc.UnresolvedThreads)
	}
}

func TestQuietMatchProducesNoRelationshipDrafts(t *testing.T) {
	view := finishedView()
	quiet := []models.Event{
		event(1, models.EventMatchEnd, "", models.VisibilityPublic, `{"outcome":"partial"}`, 5),
	}
	doc, err := Build(view, quiet, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.RelationshipUpdates) != 0 {
		t.Fatalf("quiet match invented relationship changes: %+v", doc.RelationshipUpdates)
	}
}

type pmRecorder struct {
	mu    sync.Mutex
	bodys []map[string]any
}

func (p *pmRecorder) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.bodys)
}

func (p *pmRecorder) all() []map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]map[string]any(nil), p.bodys...)
}

func newTestPort(t *testing.T, rec *pmRecorder) *services.GatewayPort {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path == "/api/v1/pm/send" {
			var parsed map[string]any
			_ = json.Unmarshal(body, &parsed)
			rec.mu.Lock()
			rec.bodys = append(rec.bodys, parsed)
			rec.mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":0,"msg":"ok","data":{"msg_id":"msg_followup_1"}}`)
	}))
	t.Cleanup(srv.Close)
	return services.NewGatewayPort(srv.URL, map[string]string{"hub_agent": "token-hub"})
}

func TestSendScopesToRealOwnersAndIsIdempotent(t *testing.T) {
	view := finishedView()
	doc, err := Build(view, events(), 1)
	if err != nil {
		t.Fatal(err)
	}
	rec := &pmRecorder{}
	sender := &Sender{Port: newTestPort(t, rec), HubAgentID: "hub_agent"}
	deliveries, err := sender.Send(context.Background(), view, doc)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(deliveries) != 2 {
		t.Fatalf("deliveries = %+v, want exactly the two real owners", deliveries)
	}
	if rec.count() != 2 {
		t.Fatalf("PM sends = %d", rec.count())
	}
	for _, got := range rec.all() {
		content, ok := got["content"].(string)
		if !ok {
			t.Fatalf("PM content is not a string: %v", got["content"])
		}
		var out Document
		if err := json.Unmarshal([]byte(content), &out); err != nil {
			t.Fatalf("follow-up PM is not structured JSON: %v", err)
		}
		if out.Kind != models.FollowUpKind || out.RecipientCompanionID == "" {
			t.Fatalf("recipient document = %+v", out)
		}
		for _, mem := range out.Memories {
			if mem.CompanionID != out.RecipientCompanionID {
				t.Fatalf("memory %s leaked to %s", mem.CompanionID, out.RecipientCompanionID)
			}
		}
		if got["receiver_id"] != "a0" && got["receiver_id"] != "a1" {
			t.Fatalf("official backfill received a PM: %v", got["receiver_id"])
		}
	}

	again, err := sender.Send(context.Background(), view, doc)
	if err != nil {
		t.Fatalf("second Send: %v", err)
	}
	if len(again) != 2 || rec.count() != 2 {
		t.Fatalf("re-send was not idempotent: deliveries=%d sends=%d", len(again), rec.count())
	}
	if len(sender.Deliveries(view.SessionID)) != 2 {
		t.Fatal("delivery record was not retained")
	}
}

func TestSendRefusesMisconfigurationAndUnfinishedMatches(t *testing.T) {
	view := finishedView()
	doc, err := Build(view, events(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (&Sender{}).Send(context.Background(), view, doc); err == nil ||
		!strings.Contains(err.Error(), "EIGENFLUX_PORT_UNCONFIGURED") {
		t.Fatalf("got %v", err)
	}
	rec := &pmRecorder{}
	if _, err := (&Sender{Port: newTestPort(t, rec)}).Send(context.Background(), view, doc); err == nil ||
		!strings.Contains(err.Error(), "HUB_AGENT_UNCONFIGURED") {
		t.Fatalf("got %v", err)
	}
	live := view
	live.Status = models.SessionLive
	if _, err := (&Sender{Port: newTestPort(t, rec), HubAgentID: "hub_agent"}).Send(context.Background(), live, doc); err == nil ||
		!strings.Contains(err.Error(), "SESSION_NOT_FINISHED") {
		t.Fatalf("got %v", err)
	}
	if rec.count() != 0 {
		t.Fatal("misconfigured sends must not touch the network")
	}
}
