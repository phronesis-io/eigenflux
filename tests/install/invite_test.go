package install_test

import (
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"eigenflux_server/tests/testutil"
)

func TestMain(m *testing.M) {
	testutil.RunTestMain(m)
}

var inviteRefRe = regexp.MustCompile(`EF-[0-9A-Za-z]{8}`)

const (
	inviterEmail = "invite_kol@test.com"
	inviteeEmail = "invite_new@test.com"
)

func cleanupInvite(t *testing.T) {
	t.Helper()
	// Remove invite codes owned by the test accounts and any tokens minted for
	// them, then the accounts themselves (CleanupTestEmails doesn't know about
	// invite tables).
	testutil.TestDB.Exec(`DELETE FROM install_tokens WHERE invite_code IN
		(SELECT code FROM invite_codes WHERE agent_id IN
			(SELECT agent_id FROM agents WHERE email IN ($1, $2)))`, inviterEmail, inviteeEmail)
	testutil.TestDB.Exec(`DELETE FROM invite_codes WHERE agent_id IN
		(SELECT agent_id FROM agents WHERE email IN ($1, $2))`, inviterEmail, inviteeEmail)
	testutil.CleanupTestEmails(t, inviterEmail, inviteeEmail)
}

// TestInviteCodeFlow covers the stable-invite-code path end to end: a KOL's
// code is auto-provisioned and visible on /agents/me, resolving /r/<code>
// mints a fresh one-shot ref, and the login-time report both keeps the funnel
// semantics and writes the registration attribution (first-wins, self-invite
// skipped).
func TestInviteCodeFlow(t *testing.T) {
	testutil.WaitForAPI(t)
	cleanupInvite(t)
	t.Cleanup(func() { cleanupInvite(t) })

	// --- KOL registers; the code is provisioned and exposed on /agents/me ---
	kolToken, kolID, _ := testutil.LoginAndGetToken(t, inviterEmail)
	me := testutil.DoGet(t, "/api/v1/agents/me", kolToken)
	profile := me["data"].(map[string]interface{})["profile"].(map[string]interface{})
	code, _ := profile["invite_code"].(string)
	if !strings.HasPrefix(code, "EFI-") || len(code) != 10 {
		t.Fatalf("expected an EFI-xxxxxx invite code on /agents/me, got %q", code)
	}

	// --- /r/<invite-code> mints a one-shot ref and serves the join doc ---
	doc := httpGet(t, testutil.BaseURL+"/r/"+code)
	ref := inviteRefRe.FindString(doc)
	if ref == "" {
		t.Fatalf("/r/<invite> join doc carries no EF- ref: %.200s", doc)
	}
	if strings.Contains(doc, code) {
		t.Fatalf("join doc must instruct the one-shot ref, not the stable invite code")
	}

	// --- install.sh-style report (no identity): converts, attributed to code ---
	rep := testutil.DoPost(t, "/api/v1/install/report", map[string]interface{}{
		"ref":      ref,
		"metadata": map[string]string{"os": "Linux", "via": "install.sh"},
	}, "")
	attr := rep["data"].(map[string]interface{})["attribution"].(map[string]interface{})
	if attr["invite_code"] != code || attr["channel"] != "kol" {
		t.Fatalf("invite mint should carry invite_code=%s channel=kol, got %v", code, attr)
	}
	if rep["data"].(map[string]interface{})["converted"] != true {
		t.Fatalf("first report should convert")
	}

	// --- the invitee registers, CLI reports the same ref with identity ---
	_, inviteeID, _ := testutil.LoginAndGetToken(t, inviteeEmail)

	// Forgery guard: a report claiming the invitee's agent_id with the wrong
	// email must not attribute (the endpoint is public; the id+email pair is
	// the proof of identity).
	testutil.DoPost(t, "/api/v1/install/report", map[string]interface{}{
		"ref": ref,
		"metadata": map[string]interface{}{
			"via": "cli", "agent_id": strconv.FormatInt(inviteeID, 10), "email": "attacker@test.com",
		},
	}, "")
	var forged string
	testutil.TestDB.QueryRow(
		`SELECT invited_by_code FROM agents WHERE agent_id = $1`, inviteeID).Scan(&forged)
	if forged != "" {
		t.Fatalf("wrong-email report must not attribute, got %q", forged)
	}

	testutil.DoPost(t, "/api/v1/install/report", map[string]interface{}{
		"ref": ref,
		"metadata": map[string]interface{}{
			"via": "cli", "agent_id": strconv.FormatInt(inviteeID, 10), "email": inviteeEmail,
		},
	}, "")
	var invitedBy string
	var inviterAgent int64
	if err := testutil.TestDB.QueryRow(
		`SELECT invited_by_code, inviter_agent_id FROM agents WHERE agent_id = $1`, inviteeID).
		Scan(&invitedBy, &inviterAgent); err != nil {
		t.Fatalf("read invitee attribution: %v", err)
	}
	if invitedBy != code || inviterAgent != kolID {
		t.Fatalf("invitee should be attributed to %s/%d, got %s/%d", code, kolID, invitedBy, inviterAgent)
	}

	// --- first-wins + registration window: a later invite ref must not
	// overwrite the attribution (the agent also registered before this ref was
	// minted, so the registered-after-entry guard rejects it independently) ---
	doc2 := httpGet(t, testutil.BaseURL+"/r/"+code)
	ref2 := inviteRefRe.FindString(doc2)
	testutil.DoPost(t, "/api/v1/install/report", map[string]interface{}{
		"ref":      ref2,
		"metadata": map[string]interface{}{"via": "cli", "agent_id": strconv.FormatInt(inviteeID, 10), "email": inviteeEmail},
	}, "")
	testutil.TestDB.QueryRow(
		`SELECT invited_by_code FROM agents WHERE agent_id = $1`, inviteeID).Scan(&invitedBy)
	if invitedBy != code {
		t.Fatalf("attribution must be first-wins, got %s", invitedBy)
	}

	// --- self-invite: the KOL reporting through their own code is not written ---
	doc3 := httpGet(t, testutil.BaseURL+"/r/"+code)
	ref3 := inviteRefRe.FindString(doc3)
	testutil.DoPost(t, "/api/v1/install/report", map[string]interface{}{
		"ref":      ref3,
		"metadata": map[string]interface{}{"via": "cli", "agent_id": strconv.FormatInt(kolID, 10), "email": inviterEmail},
	}, "")
	var kolInvitedBy string
	testutil.TestDB.QueryRow(
		`SELECT invited_by_code FROM agents WHERE agent_id = $1`, kolID).Scan(&kolInvitedBy)
	if kolInvitedBy != "" {
		t.Fatalf("self-invite must not attribute, got %q", kolInvitedBy)
	}

	// --- landing-page path: mint with ?ic= carries the code through ---
	mint := testutil.DoPost(t, "/api/v1/install/token", map[string]interface{}{
		"utm_source":  "redskills",
		"invite_code": code,
	}, "")
	md := mint["data"].(map[string]interface{})
	rep4 := testutil.DoPost(t, "/api/v1/install/report",
		map[string]interface{}{"ref": md["ref"]}, "")
	attr4 := rep4["data"].(map[string]interface{})["attribution"].(map[string]interface{})
	if attr4["invite_code"] != code {
		t.Fatalf("?ic= mint should carry invite_code, got %v", attr4)
	}
	// The explicit platform source wins the channel bucket; the invite code
	// still attributes the KOL.
	if attr4["channel"] != "redskills" {
		t.Fatalf("explicit utm_source should keep the platform channel, got %v", attr4["channel"])
	}
}

// TestInviteCodeUnknown ensures unknown or malformed invite entries degrade
// cleanly: /r/ serves 404 for an unknown code, and a mint with a bogus ?ic=
// still succeeds unattributed.
func TestInviteCodeUnknown(t *testing.T) {
	testutil.WaitForAPI(t)

	resp, err := http.Get(testutil.BaseURL + "/r/EFI-zzzzzz")
	if err != nil {
		t.Fatalf("GET /r/EFI-zzzzzz: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown invite code should 404, got %d: %s", resp.StatusCode, body)
	}

	mint := testutil.DoPost(t, "/api/v1/install/token", map[string]interface{}{
		"utm_source":  "redskills",
		"invite_code": "EFI-zzzzzz",
	}, "")
	if int(mint["code"].(float64)) != 0 {
		t.Fatalf("mint with unknown ?ic= should still succeed: %v", mint)
	}
	ref := mint["data"].(map[string]interface{})["ref"].(string)
	rep := testutil.DoPost(t, "/api/v1/install/report", map[string]interface{}{"ref": ref}, "")
	attr := rep["data"].(map[string]interface{})["attribution"].(map[string]interface{})
	if attr["invite_code"] != "" {
		t.Fatalf("unknown ?ic= must not attribute, got %v", attr["invite_code"])
	}
}
