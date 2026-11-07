package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"fluxspace.ai/hub/followup"
	invitationpkg "fluxspace.ai/hub/invitation"
	"fluxspace.ai/hub/models"
	"fluxspace.ai/hub/services"
	"fluxspace.ai/hub/session"
)

// s1Gateway is a real HTTP gateway speaking the EigenFlux envelope. When it
// receives an invitation PM it parses the structured content and enqueues an
// accept from the responding Agent, so the test exercises send -> receive ->
// parse -> reply -> fetch -> parse -> confirm over two real HTTP hops rather
// than by calling the manager directly.
type s1Gateway struct {
	mu       sync.Mutex
	replies  []map[string]any
	delivery []map[string]any
	seq      int
}

func (g *s1Gateway) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		ok := func(data string) {
			_, _ = io.WriteString(w, `{"code":0,"msg":"ok","data":`+data+`}`)
		}
		switch r.URL.Path {
		case "/api/v1/relations/apply":
			ok(`{"request_id":"req_1"}`)
		case "/api/v1/pm/send":
			var req struct {
				ReceiverID string `json:"receiver_id"`
				Content    string `json:"content"`
			}
			_ = json.Unmarshal(body, &req)
			g.mu.Lock()
			g.seq++
			seq := g.seq
			g.delivery = append(g.delivery, map[string]any{
				"receiver_id": req.ReceiverID, "content": req.Content,
			})
			var doc struct {
				Kind             string `json:"kind"`
				InvitationID     string `json:"invitation_id"`
				FromAgentID      string `json:"from_agent_id"`
				ToAgentID        string `json:"to_agent_id"`
				SelectedProposal int    `json:"selected_proposal"`
			}
			_ = json.Unmarshal([]byte(req.Content), &doc)
			if doc.Kind == models.InvitationKind {
				reply, _ := json.Marshal(models.InvitationReply{
					Kind: models.ReplyKind, InvitationID: doc.InvitationID,
					Action: models.ReplyAccept, AgentID: doc.ToAgentID,
					SelectedProposal: doc.SelectedProposal,
				})
				g.replies = append(g.replies, map[string]any{
					"msg_id": "msg_reply_1", "conv_id": "conv_1",
					"sender_id": doc.ToAgentID, "receiver_id": doc.FromAgentID,
					"content": string(reply), "is_read": false, "created_at": int64(1),
				})
			}
			g.mu.Unlock()
			buf, _ := json.Marshal(map[string]string{"msg_id": "msg_" + strconv.Itoa(seq), "conv_id": "conv_1"})
			ok(string(buf))
		case "/api/v1/pm/fetch":
			g.mu.Lock()
			messages := append([]map[string]any(nil), g.replies...)
			g.mu.Unlock()
			buf, _ := json.Marshal(map[string]any{"messages": messages, "next_cursor": "cursor_1"})
			ok(string(buf))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func (g *s1Gateway) followUpCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := 0
	for _, d := range g.delivery {
		if c, _ := d["content"].(string); strings.Contains(c, models.FollowUpKind) {
			n++
		}
	}
	return n
}

func newS1Server(t *testing.T, gw *s1Gateway) *httptest.Server {
	t.Helper()
	gatewaySrv := httptest.NewServer(gw.handler())
	t.Cleanup(gatewaySrv.Close)
	port := services.NewGatewayPort(gatewaySrv.URL, map[string]string{
		"agent_alpha": "token-alpha", "agent_beta": "token-beta", "hub_agent": "token-hub",
	})
	mgr := session.NewManagerWithClock(func() int64 { return 1700000000000 })
	srv := &Server{
		Manager: mgr, SpectatorToken: "spectator-secret",
		PollInterval: 5 * time.Millisecond,
		Invitations:  invitationpkg.NewManagerWithClock(func() int64 { return 1700000000000 }),
		Port:         port,
		FollowUp:     &followup.Sender{Port: port, HubAgentID: "hub_agent"},
		Clock:        func() int64 { return 1700000000123 },
	}
	mux := http.NewServeMux()
	RegisterHealth(mux, srv)
	RegisterSessions(mux, srv)
	RegisterInvitations(mux, srv)
	RegisterFollowUps(mux, srv)
	hub := httptest.NewServer(mux)
	t.Cleanup(hub.Close)
	return hub
}

func runMatchWithViewSeats(t *testing.T, srv *httptest.Server, view models.SessionView) models.SessionView {
	t.Helper()
	seats := view.Seats
	for step := 0; step < 20; step++ {
		var current models.SessionView
		call(t, http.MethodGet, srv.URL+"/hub/sessions/"+view.SessionID, nil, http.StatusOK, &current)
		if current.Status == models.SessionFinished {
			return current
		}
		call(t, http.MethodPost, srv.URL+"/hub/sessions/"+view.SessionID+"/advance",
			map[string]string{"phase": "discuss"}, http.StatusOK, &current)
		call(t, http.MethodPost, srv.URL+"/hub/sessions/"+view.SessionID+"/discussion",
			models.DiscussionMessage{SeatID: seats[0].SeatID, Text: "The ledger disagrees with the timetable.", Channel: "public"},
			http.StatusAccepted, nil)
		call(t, http.MethodPost, srv.URL+"/hub/sessions/"+view.SessionID+"/advance",
			map[string]string{"phase": "act"}, http.StatusOK, &current)
		actionID := current.Skeleton.Rounds[current.Round-1].Actions[0].ID
		for _, seat := range seats {
			call(t, http.MethodPost, srv.URL+"/hub/sessions/"+view.SessionID+"/actions", map[string]any{
				"seat_id": seat.SeatID, "action_id": actionID,
				"reasoning_summary": "rule engine selected a legal action",
				"confidence":        70, "source": "rule_engine",
			}, http.StatusOK, &current)
		}
	}
	t.Fatal("match did not finish over HTTP")
	return models.SessionView{}
}

// TestS1FullLoopOverHTTP is the vertical slice S1 was defined to prove:
// a structured PM invitation is sent and answered, the confirmed negotiation
// creates a seat-isolated match, five rounds settle, and a follow-up is
// delivered to the real owners. The gateway here is a local HTTP stand-in;
// the live EigenFlux stack remains G10.
func TestS1FullLoopOverHTTP(t *testing.T) {
	gw := &s1Gateway{}
	srv := newS1Server(t, gw)
	base := srv.URL

	var neg invitationpkg.Negotiation
	call(t, http.MethodPost, base+"/hub/invitations", invitationpkg.Request{
		Initiator: invitationpkg.Party{
			AgentID: "agent_alpha", CompanionID: "comp_alpha",
			OwnerID: "owner_alpha", DisplayName: "Lin Shen",
		},
		Responder: invitationpkg.Party{
			AgentID: "agent_beta", CompanionID: "comp_beta",
			OwnerID: "owner_beta", DisplayName: "Su Wan",
		},
		Seats: 4, Seed: 42, TTLSeconds: 300,
	}, http.StatusCreated, &neg)
	if neg.Status != invitationpkg.StatusDraft || len(neg.Invitation.Proposals) != 3 {
		t.Fatalf("negotiation = %+v", neg)
	}

	var sent struct {
		Invitation invitationpkg.Negotiation `json:"invitation"`
		MessageID  string                    `json:"message_id"`
	}
	call(t, http.MethodPost, base+"/hub/invitations/"+neg.ID+"/send", nil, http.StatusOK, &sent)
	if sent.MessageID == "" || sent.Invitation.Status != invitationpkg.StatusProposed {
		t.Fatalf("send = %+v", sent)
	}

	var polled struct {
		Invitation invitationpkg.Negotiation `json:"invitation"`
		Applied    int                       `json:"applied_replies"`
	}
	call(t, http.MethodPost, base+"/hub/invitations/"+neg.ID+"/poll", nil, http.StatusOK, &polled)
	if polled.Applied != 1 || polled.Invitation.Status != invitationpkg.StatusConfirmed {
		t.Fatalf("poll = %+v", polled)
	}
	if len(polled.Invitation.Inbox) != 1 {
		t.Fatal("fetched PM must be persisted before interpretation")
	}

	var created struct {
		Session models.SessionView `json:"session"`
	}
	call(t, http.MethodPost, base+"/hub/invitations/"+neg.ID+"/session",
		map[string]any{"seed": int64(42)}, http.StatusCreated, &created)
	view := created.Session
	if len(view.Seats) != 4 || view.Status != models.SessionLobby {
		t.Fatalf("session = %+v", view)
	}
	for _, seat := range view.Seats {
		call(t, http.MethodPost, base+"/hub/sessions/"+view.SessionID+"/join",
			map[string]string{"seat_id": seat.SeatID, "owner_id": seat.OwnerID}, http.StatusOK, nil)
	}
	finished := runMatchWithViewSeats(t, srv, view)
	if finished.Status != models.SessionFinished || finished.Outcome == "" {
		t.Fatalf("outcome = %q status = %s", finished.Outcome, finished.Status)
	}

	var doc followup.Document
	call(t, http.MethodGet, base+"/hub/sessions/"+view.SessionID+"/followup", nil, http.StatusOK, &doc)
	if doc.Kind != models.FollowUpKind || len(doc.Memories) == 0 {
		t.Fatalf("follow-up = %+v", doc)
	}
	for _, mem := range doc.Memories {
		if mem.Source.RefID == "" || mem.Source.Kind == "" {
			t.Fatalf("memory is not source-tagged: %+v", mem)
		}
	}

	var delivered struct {
		Deliveries []followup.Delivery `json:"deliveries"`
	}
	call(t, http.MethodPost, base+"/hub/sessions/"+view.SessionID+"/followup", nil, http.StatusOK, &delivered)
	if len(delivered.Deliveries) != 2 {
		t.Fatalf("deliveries = %+v, want the two real owners only", delivered.Deliveries)
	}
	if gw.followUpCount() != 2 {
		t.Fatalf("follow-up PMs on the wire = %d", gw.followUpCount())
	}
	call(t, http.MethodPost, base+"/hub/sessions/"+view.SessionID+"/followup", nil, http.StatusOK, &delivered)
	if gw.followUpCount() != 2 {
		t.Fatal("follow-up re-send was not idempotent")
	}
}

func TestGatewayEndpointsFailLoudlyWhenUnconfigured(t *testing.T) {
	mgr := session.NewManager()
	mux := http.NewServeMux()
	srvCfg := &Server{Manager: mgr, Invitations: invitationpkg.NewManager()}
	RegisterSessions(mux, srvCfg)
	RegisterInvitations(mux, srvCfg)
	RegisterFollowUps(mux, srvCfg)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var neg invitationpkg.Negotiation
	call(t, http.MethodPost, srv.URL+"/hub/invitations", invitationpkg.Request{
		Initiator: invitationpkg.Party{AgentID: "a", OwnerID: "o1", CompanionID: "c1"},
		Responder: invitationpkg.Party{AgentID: "b", OwnerID: "o2", CompanionID: "c2"},
		Seats:     2, Seed: 7,
	}, http.StatusCreated, &neg)
	var out map[string]any
	call(t, http.MethodPost, srv.URL+"/hub/invitations/"+neg.ID+"/send", nil,
		http.StatusServiceUnavailable, &out)
	if !strings.Contains(out["error"].(map[string]any)["code"].(string), "EIGENFLUX_PORT_UNCONFIGURED") {
		t.Fatalf("error = %v", out)
	}
}
