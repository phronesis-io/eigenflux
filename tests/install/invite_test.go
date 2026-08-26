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
var shortIDRe = regexp.MustCompile(`^[A-Za-z]{5}$`)

const (
	inviterEmail = "invite_kol@test.com"
	inviteeEmail = "invite_new@test.com"
	paidEmail    = "invite_paid@test.com"
	legacyCode   = "EFI-tE5tAa"
)

func cleanupInvite(t *testing.T) {
	t.Helper()
	// Remove invite codes owned by the test accounts and any tokens minted for
	// them, then the accounts themselves (CleanupTestEmails doesn't know about
	// invite tables).
	testutil.TestDB.Exec(`DELETE FROM install_tokens WHERE invite_code = $1 OR invite_code IN
		(SELECT code FROM invite_codes WHERE agent_id IN
			(SELECT agent_id FROM agents WHERE email IN ($2, $3))) OR invite_code IN
		(SELECT short_id FROM agents WHERE email IN ($2, $3))`, legacyCode, inviterEmail, inviteeEmail)
	testutil.TestDB.Exec(`DELETE FROM invite_codes WHERE agent_id IN
		(SELECT agent_id FROM agents WHERE email IN ($1, $2))`, inviterEmail, inviteeEmail)
	testutil.CleanupTestEmails(t, inviterEmail, inviteeEmail, paidEmail)
}

// TestInviteCodeFlow covers the public short-ID path end to end: an Agent's
// immutable short ID is visible on /agents/me, resolving /r/<short-id>
// mints a fresh one-shot ref, and the login-time report both keeps the funnel
// semantics and writes the registration attribution (first-wins, self-invite
// skipped).
func TestInviteCodeFlow(t *testing.T) {
	testutil.WaitForAPI(t)
	cleanupInvite(t)
	t.Cleanup(func() { cleanupInvite(t) })

	// --- Agent registers; the short ID is additive while the V1 invite_code
	// remains the stable EFI attribution code expected by existing clients. ---
	kolToken, kolID, _ := testutil.LoginAndGetToken(t, inviterEmail)
	me := testutil.DoGet(t, "/api/v1/agents/me", kolToken)
	profile := me["data"].(map[string]interface{})["profile"].(map[string]interface{})
	code, _ := profile["short_id"].(string)
	if !shortIDRe.MatchString(code) {
		t.Fatalf("expected a case-sensitive five-letter short_id on /agents/me, got %q", code)
	}
	legacyCode, _ := profile["invite_code"].(string)
	if !strings.HasPrefix(legacyCode, "EFI-") {
		t.Fatalf("expected legacy EFI invite_code on /agents/me, got %q", legacyCode)
	}
	var personalEFICodes int
	if err := testutil.TestDB.QueryRow(`SELECT count(*) FROM invite_codes WHERE kind = 'kol' AND agent_id = $1`, kolID).Scan(&personalEFICodes); err != nil {
		t.Fatalf("count personal EFI codes: %v", err)
	}
	if personalEFICodes != 1 {
		t.Fatalf("V1 compatibility must retain one personal EFI code, got %d", personalEFICodes)
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
	if attr["invite_code"] != code || attr["channel"] != "user" {
		t.Fatalf("invite mint should carry invite_code=%s channel=user, got %v", code, attr)
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
	// The same verified report also pins the user-level acquisition channel.
	var acq string
	testutil.TestDB.QueryRow(
		`SELECT acquisition_channel FROM agents WHERE agent_id = $1`, inviteeID).Scan(&acq)
	if acq != "user" {
		t.Fatalf("invitee acquisition_channel should be 'user', got %q", acq)
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

	// --- historical personal EFI links remain readable until revoked. ---
	if _, err := testutil.TestDB.Exec(`INSERT INTO invite_codes(code, kind, agent_id, name, note, created_at)
		VALUES ($1, 'kol', $2, '', 'legacy compatibility fixture', extract(epoch from now())::bigint * 1000)`, legacyCode, kolID); err != nil {
		t.Fatalf("insert historical personal EFI code: %v", err)
	}
	legacyDoc := httpGet(t, testutil.BaseURL+"/r/"+legacyCode)
	if inviteRefRe.FindString(legacyDoc) == "" {
		t.Fatalf("historical personal EFI link did not resolve")
	}
	if _, err := testutil.TestDB.Exec(`UPDATE invite_codes SET revoked_at = extract(epoch from now())::bigint * 1000 WHERE code = $1`, legacyCode); err != nil {
		t.Fatalf("revoke historical personal EFI code: %v", err)
	}
	legacyResp, err := http.Get(testutil.BaseURL + "/r/" + legacyCode)
	if err != nil {
		t.Fatalf("GET revoked historical link: %v", err)
	}
	legacyResp.Body.Close()
	if legacyResp.StatusCode != http.StatusNotFound {
		t.Fatalf("revoked historical personal EFI link should 404, got %d", legacyResp.StatusCode)
	}

	// --- paid path (no invite code): the verified report pins the platform
	// channel onto the user, leaving invite attribution empty ---
	paidMint := testutil.DoPost(t, "/api/v1/install/token", map[string]interface{}{
		"utm_source": "xiaohongshu",
	}, "")
	paidRef := paidMint["data"].(map[string]interface{})["ref"].(string)
	_, paidID, _ := testutil.LoginAndGetToken(t, paidEmail)
	testutil.DoPost(t, "/api/v1/install/report", map[string]interface{}{
		"ref": paidRef,
		"metadata": map[string]interface{}{
			"via": "cli", "agent_id": strconv.FormatInt(paidID, 10), "email": paidEmail,
		},
	}, "")
	var paidAcq, paidInvited string
	testutil.TestDB.QueryRow(
		`SELECT acquisition_channel, invited_by_code FROM agents WHERE agent_id = $1`, paidID).
		Scan(&paidAcq, &paidInvited)
	if paidAcq != "xiaohongshu" || paidInvited != "" {
		t.Fatalf("paid signup should pin channel xiaohongshu with no invite, got %q/%q", paidAcq, paidInvited)
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
