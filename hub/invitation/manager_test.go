package invitation

import (
	"context"
	"encoding/json"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"fluxspace.ai/hub/generator"
	"fluxspace.ai/hub/models"
	"fluxspace.ai/hub/services"
	"fluxspace.ai/hub/session"
)

const (
	initiatorAgent = "agent_alpha"
	responderAgent = "agent_beta"
)

// fakeGateway is a real HTTP server speaking the gateway envelope. It is not a
// mock of EigenFluxPort: every call goes through services.GatewayPort and its
// real HTTP client. The live EigenFlux stack is still G10.
type fakeGateway struct {
	mu       sync.Mutex
	sent     []map[string]any
	replies  []map[string]any
	fetchCnt int
}

func (f *fakeGateway) queueReply(t *testing.T, msgID string, reply models.InvitationReply) {
	t.Helper()
	b, err := json.Marshal(reply)
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replies = append(f.replies, map[string]any{
		"msg_id": msgID, "conv_id": "conv_1",
		"sender_id": responderAgent, "receiver_id": initiatorAgent,
		"content": string(b), // IDL: content is a JSON string
		"is_read": false, "created_at": int64(1),
	})
}

func (f *fakeGateway) sentBodies() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.sent...)
}

func (f *fakeGateway) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/relations/apply":
			_, _ = io.WriteString(w, `{"code":0,"msg":"ok","data":{"request_id":"req_1"}}`)
		case "/api/v1/pm/send":
			var parsed map[string]any
			_ = json.Unmarshal(body, &parsed)
			f.mu.Lock()
			f.sent = append(f.sent, parsed)
			f.mu.Unlock()
			_, _ = io.WriteString(w, `{"code":0,"msg":"ok","data":{"msg_id":"msg_inv_1","conv_id":"conv_1"}}`)
		case "/api/v1/pm/fetch":
			f.mu.Lock()
			f.fetchCnt++
			messages := append([]map[string]any(nil), f.replies...)
			f.mu.Unlock()
			b, _ := json.Marshal(map[string]any{"messages": messages, "next_cursor": "cursor_1"})
			_, _ = io.WriteString(w, `{"code":0,"msg":"ok","data":`+string(b)+`}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func newPort(t *testing.T, fake *fakeGateway) *services.GatewayPort {
	t.Helper()
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)
	return services.NewGatewayPort(srv.URL, map[string]string{
		initiatorAgent: "token-alpha", responderAgent: "token-beta",
	})
}

func newManager() *Manager {
	now := int64(1730000000000)
	return NewManagerWithClock(func() int64 { return now })
}

func baseRequest() Request {
	return Request{
		Initiator: Party{
			AgentID: initiatorAgent, CompanionID: "comp_alpha",
			OwnerID: "owner_alpha", DisplayName: "Lin Shen",
		},
		Responder: Party{
			AgentID: responderAgent, CompanionID: "comp_beta",
			OwnerID: "owner_beta", DisplayName: "Su Wan",
		},
		Seats: 4, Seed: 42,
	}
}

func TestCreateGeneratesThreeDistinctProposals(t *testing.T) {
	m := newManager()
	n, err := m.Create(baseRequest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if n.Status != StatusDraft || len(n.Invitation.Proposals) != ProposalCount {
		t.Fatalf("status=%s proposals=%d", n.Status, len(n.Invitation.Proposals))
	}
	if n.Invitation.Kind != models.InvitationKind || n.Invitation.SelectedProposal != 1 {
		t.Fatalf("invitation = %+v", n.Invitation)
	}
	if !n.Initiator.Confirmed || n.Responder.Confirmed {
		t.Fatal("only the initiator should be confirmed before a reply")
	}
	seen := map[string]bool{}
	for i, p := range n.Invitation.Proposals {
		if p.Index != i+1 || p.StructuralHash == "" || p.Minutes < 10 {
			t.Fatalf("proposal %d = %+v", i, p)
		}
		// StructuralHash is topology-only and may legitimately collide;
		// distinct fingerprints/content are what makes three real choices.
		if seen[p.ContentHash] {
			t.Fatalf("duplicate proposal content %s", p.ContentHash)
		}
		seen[p.ContentHash] = true
		if len(p.ContractPoints) == 0 {
			t.Fatal("proposal has no social-contract points")
		}
	}
	if n.Invitation.ExpiresAt <= n.CreatedAt {
		t.Fatal("invitation has no TTL")
	}
}

func TestCreateRejectsSameOwnerAndUnsupportedSeatCount(t *testing.T) {
	m := newManager()
	req := baseRequest()
	req.Responder.OwnerID = req.Initiator.OwnerID
	if _, err := m.Create(req); err == nil || !strings.Contains(err.Error(), "OWNER_SEAT_CONFLICT") {
		t.Fatalf("got %v", err)
	}
	req = baseRequest()
	req.Seats = 8
	if _, err := m.Create(req); err == nil || !strings.Contains(err.Error(), "INVALID_SEAT_COUNT") {
		t.Fatalf("got %v", err)
	}
}

// TestRealPMRoundTripClosesNegotiation is the receive half S1 was missing:
// Send -> gateway PM -> fetch -> parse -> apply -> all confirmed -> claims ->
// a real Hub session. Every network hop is a real HTTP request.
func TestRealPMRoundTripClosesNegotiation(t *testing.T) {
	m := newManager()
	fake := &fakeGateway{}
	port := newPort(t, fake)
	n, err := m.Create(baseRequest())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	sent, msgID, err := m.Send(context.Background(), port, n.ID)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if sent.Status != StatusProposed || msgID != "msg_inv_1" {
		t.Fatalf("status=%s msgID=%s", sent.Status, msgID)
	}
	bodies := fake.sentBodies()
	if len(bodies) != 1 {
		t.Fatalf("PM sends = %d", len(bodies))
	}
	content, isString := bodies[0]["content"].(string)
	if !isString {
		t.Fatalf("PM content must be a string, got %T", bodies[0]["content"])
	}
	var delivered models.Invitation
	if err := json.Unmarshal([]byte(content), &delivered); err != nil {
		t.Fatalf("delivered PM is not a structured invitation: %v", err)
	}
	if delivered.InvitationID != n.ID || len(delivered.Proposals) != 3 {
		t.Fatalf("delivered = %+v", delivered)
	}

	// Responder answers from its own Agent identity, over PM.
	fake.queueReply(t, "msg_reply_1", models.InvitationReply{
		Kind: models.ReplyKind, InvitationID: n.ID,
		Action: models.ReplyAccept, AgentID: responderAgent, SelectedProposal: 2,
	})
	afterPoll, applied, err := m.PollReplies(context.Background(), port, n.ID)
	if err != nil {
		t.Fatalf("PollReplies: %v", err)
	}
	if applied != 1 || afterPoll.Status != StatusNegotiating {
		t.Fatalf("counter-proposal: applied=%d status=%s", applied, afterPoll.Status)
	}
	if len(afterPoll.Inbox) != 1 {
		t.Fatal("fetched messages must be persisted before interpretation")
	}

	// The initiator must explicitly confirm the changed index.
	confirmed, err := m.ApplyReply(n.ID, "local_confirm", models.InvitationReply{
		Kind: models.ReplyKind, InvitationID: n.ID,
		Action: models.ReplyAccept, AgentID: initiatorAgent, SelectedProposal: 2,
	})
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if confirmed.Status != StatusConfirmed || confirmed.Invitation.SelectedProposal != 2 {
		t.Fatalf("status=%s selected=%d", confirmed.Status, confirmed.Invitation.SelectedProposal)
	}
	if _, already := confirmed.applied["msg_reply_1"]; !already {
		t.Fatal("reply idempotency map was lost")
	}

	// Idempotent re-poll: the same PM must not be applied twice.
	repolled, again, err := m.PollReplies(context.Background(), port, n.ID)
	if err != nil || again != 0 {
		t.Fatalf("re-poll applied %d replies (err=%v)", again, err)
	}
	if len(repolled.Inbox) != 1 {
		t.Fatalf("redelivered PM was stored twice: inbox has %d entries", len(repolled.Inbox))
	}

	inv, claims, err := m.BuildClaims(n.ID, nil)
	if err != nil {
		t.Fatalf("BuildClaims: %v", err)
	}
	if inv.SelectedProposal != 2 || len(claims) != 4 {
		t.Fatalf("inv.selected=%d claims=%d", inv.SelectedProposal, len(claims))
	}
	if claims[0].Official || claims[1].Official {
		t.Fatal("real owners must not be labelled official")
	}
	for _, c := range claims[2:] {
		if !c.Official || !strings.Contains(c.DisplayName, "backfill") {
			t.Fatalf("unlabelled backfill: %+v", c)
		}
	}

	sm := session.NewManager()
	view, err := sm.Create(session.CreateRequest{
		Invitation: inv, Claims: claims, Seed: n.Seed,
		CompanionCount: 4, Pace: "standard", Mode: "coop",
	})
	if err != nil {
		t.Fatalf("session.Create from confirmed negotiation: %v", err)
	}
	if view.Status != models.SessionLobby || view.TotalRounds != 5 {
		t.Fatalf("view = %+v", view)
	}
	if err := m.MarkSessionCreated(n.ID, view.SessionID); err != nil {
		t.Fatalf("MarkSessionCreated: %v", err)
	}
	if err := m.MarkSessionCreated(n.ID, view.SessionID); err == nil {
		t.Fatal("a confirmed invitation must not spawn a second match")
	}
}

func TestRejectIsTerminalAndModifyRegeneratesProposals(t *testing.T) {
	m := newManager()
	n, err := m.Create(baseRequest())
	if err != nil {
		t.Fatal(err)
	}
	before := n.Invitation.Proposals[0].ContentHash

	modified, err := m.ApplyReply(n.ID, "", models.InvitationReply{
		Kind: models.ReplyKind, InvitationID: n.ID, Action: models.ReplyModify,
		AgentID: responderAgent, Amendments: []string{"fewer clues"},
	})
	if err != nil {
		t.Fatalf("modify: %v", err)
	}
	if modified.Status != StatusNegotiating || modified.ModifyRounds != 1 {
		t.Fatalf("status=%s rounds=%d", modified.Status, modified.ModifyRounds)
	}
	if modified.Invitation.Proposals[0].ContentHash == before {
		t.Fatal("modify must regenerate the proposal set")
	}
	if modified.Responder.Confirmed || len(modified.Responder.Amendments) != 1 {
		t.Fatalf("responder = %+v", modified.Responder)
	}

	for i := 0; i < MaxModifyRounds; i++ {
		_, err := m.ApplyReply(n.ID, "", models.InvitationReply{
			Kind: models.ReplyKind, InvitationID: n.ID, Action: models.ReplyModify, AgentID: responderAgent,
		})
		if i < MaxModifyRounds-1 && err != nil {
			t.Fatalf("modify %d: %v", i, err)
		}
		if i == MaxModifyRounds-1 && (err == nil || !strings.Contains(err.Error(), "MODIFY_LIMIT_REACHED")) {
			t.Fatalf("expected modify limit, got %v", err)
		}
	}

	rejected, err := m.Create(baseRequest())
	if err != nil {
		t.Fatal(err)
	}
	done, err := m.ApplyReply(rejected.ID, "", models.InvitationReply{
		Kind: models.ReplyKind, InvitationID: rejected.ID,
		Action: models.ReplyReject, AgentID: responderAgent,
	})
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	if done.Status != StatusRejected {
		t.Fatalf("status = %s", done.Status)
	}
	if _, err := m.ApplyReply(rejected.ID, "", models.InvitationReply{
		Kind: models.ReplyKind, InvitationID: rejected.ID,
		Action: models.ReplyAccept, AgentID: responderAgent,
	}); err == nil || !strings.Contains(err.Error(), "INVITATION_TERMINAL") {
		t.Fatalf("terminal negotiation accepted a reply: %v", err)
	}
}

func TestExpiryAndInvalidReplyAreNotSilentlyIgnored(t *testing.T) {
	now := int64(1730000000000)
	m := NewManagerWithClock(func() int64 { return now })
	n, err := m.Create(baseRequest())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.ApplyReply(n.ID, "", models.InvitationReply{
		Kind: "wrong.kind", InvitationID: n.ID, Action: models.ReplyAccept, AgentID: responderAgent,
	}); err == nil || !strings.Contains(err.Error(), "INVALID_REPLY_KIND") {
		t.Fatalf("got %v", err)
	}
	if _, err := m.ApplyReply(n.ID, "", models.InvitationReply{
		Kind: models.ReplyKind, InvitationID: n.ID, Action: models.ReplyAccept,
		AgentID: "stranger",
	}); err == nil || !strings.Contains(err.Error(), "UNKNOWN_REPLY_AGENT") {
		t.Fatalf("got %v", err)
	}
	if _, err := m.ApplyReply(n.ID, "", models.InvitationReply{
		Kind: models.ReplyKind, InvitationID: n.ID, Action: models.ReplyAccept,
		AgentID: responderAgent, SelectedProposal: 99,
	}); err == nil || !strings.Contains(err.Error(), "INVALID_SELECTED_PROPOSAL") {
		t.Fatalf("got %v", err)
	}
	now += int64(DefaultTTLSeconds)*1000 + 1
	if _, err := m.ApplyReply(n.ID, "", models.InvitationReply{
		Kind: models.ReplyKind, InvitationID: n.ID, Action: models.ReplyAccept, AgentID: responderAgent,
	}); err == nil || !strings.Contains(err.Error(), "INVITATION_EXPIRED") {
		t.Fatalf("got %v", err)
	}
	expired, err := m.Get(n.ID)
	if err != nil || expired.Status != StatusExpired {
		t.Fatalf("status = %s err = %v", expired.Status, err)
	}
}

func TestBuildClaimsRequiresConfirmation(t *testing.T) {
	m := newManager()
	n, err := m.Create(baseRequest())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.BuildClaims(n.ID, nil); err == nil || !strings.Contains(err.Error(), "INVITATION_NOT_CONFIRMED") {
		t.Fatalf("got %v", err)
	}
}

// TestCreateExcludesLastFiveMatchFingerprints is the S2 acceptance check:
// "structural fingerprints do not repeat within the last 5 matches". It builds
// a five-match history out of exactly the skeletons buildProposals would have
// emitted for this seed (same rand.NewSource sequence), then asserts the three
// returned proposals avoid all five, and that a modify-round regeneration
// (rotation 1) avoids them too.
func TestCreateExcludesLastFiveMatchFingerprints(t *testing.T) {
	m := newManager()
	req := baseRequest()

	history := make([]string, 0, 5)
	for attempt := 0; attempt < 5; attempt++ {
		sk, err := generator.GenerateSkeleton(generator.GenerateOptions{
			Pace: req.Pace, Mode: req.Mode, CompanionCount: req.Seats,
		}, rand.New(rand.NewSource(req.Seed+int64(attempt)*104729)))
		if err != nil {
			t.Fatalf("generate history skeleton %d: %v", attempt, err)
		}
		history = append(history, sk.Fingerprint)
	}
	banned := make(map[string]bool, len(history))
	for _, fp := range history {
		banned[fp] = true
	}
	// Sanity: the history must be five distinct fingerprints, otherwise the
	// assertions below would be vacuous.
	if len(banned) != 5 {
		t.Fatalf("history is not five distinct fingerprints: %v", history)
	}

	req.HistoryFingerprints = history
	n, err := m.Create(req)
	if err != nil {
		t.Fatalf("Create with history: %v", err)
	}
	if len(n.HistoryFingerprints) != 5 {
		t.Fatalf("negotiation kept %d history fingerprints, want 5", len(n.HistoryFingerprints))
	}
	for _, p := range n.Invitation.Proposals {
		if p.Fingerprint == "" {
			t.Fatalf("proposal %d exposes no fingerprint, so no client could ever supply history", p.Index)
		}
		if banned[p.Fingerprint] {
			t.Fatalf("proposal %d reused a fingerprint from the last five matches: %s", p.Index, p.Fingerprint)
		}
	}

	// A modify round re-generates proposals from the retained history; it must
	// respect the same window (this is the path applyReplyLocked takes).
	rotated, err := buildProposals(req.Seed, req.Seats, req.Pace, req.Mode,
		models.SocialContract{}, n.HistoryFingerprints, 1)
	if err != nil {
		t.Fatalf("modify-round regeneration: %v", err)
	}
	for _, p := range rotated {
		if banned[p.Fingerprint] {
			t.Fatalf("modify round reused a fingerprint from the last five matches: %s", p.Fingerprint)
		}
	}

	// Omitting the field must keep the old behaviour: no history, no exclusion.
	plain, err := m.Create(baseRequest())
	if err != nil {
		t.Fatalf("Create without history: %v", err)
	}
	if len(plain.HistoryFingerprints) != 0 {
		t.Fatalf("default request invented history: %v", plain.HistoryFingerprints)
	}
}
