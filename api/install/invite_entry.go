package install

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/invite"
	"eigenflux_server/pkg/logger"
)

// This file bridges stable invite codes (pkg/invite, EFI-xxxxxx) into the
// one-shot install-token funnel: an invite entry mints a token carrying
// invite_code, and a later install report that carries the agent identity
// writes the registration attribution (agents.invited_by_code) first-wins.

// botUARe matches link-preview unfurlers and crawlers. Invite links are shared
// raw in public posts, and every /r/<invite> fetch mints a funnel row — bots
// must not inflate the landing metrics the KOL leaderboard is built on. (The
// server-minted EF- path has no such exposure: its /r/ fetch only stamps an
// existing row.)
var botUARe = regexp.MustCompile(`(?i)bot|crawl|spider|preview|slurp|headless|facebookexternalhit|whatsapp|telegram|slack|discord|skype|micromessenger|embedly|pinterest|vkshare`)

// isPreviewBot reports whether a /r/<invite> fetch looks like a link unfurler
// rather than an agent following the join instructions. Biased toward minting
// (only clearly bot-identified UAs are excluded) so no real agent entry is
// ever dropped.
func isPreviewBot(userAgent string) bool {
	return botUARe.MatchString(userAgent)
}

// lookupInviteCode resolves a user-supplied invite code to its row, returning
// nil for anything malformed or unknown (the caller degrades to unattributed).
func lookupInviteCode(code string) *invite.Code {
	code = strings.TrimSpace(code)
	if code == "" || !invite.ValidFormat(code) {
		return nil
	}
	ic, err := invite.GetByCode(db.DB, code)
	if err != nil {
		logger.Default().Warn("install", "ev", "invite_lookup_error", "code", code, "err", err.Error())
		return nil
	}
	if ic == nil {
		event("install_invite_unknown", code)
	}
	return ic
}

// mintForInvite mints the one-shot token for a direct /r/<invite-code> entry.
// There is no landing page in this path, so the fetch itself is the entry:
// fetched_at is stamped at creation and the funnel proceeds as usual.
func mintForInvite(ic *invite.Code, referrer, ip string) (*Token, error) {
	now := time.Now().UnixMilli()
	t := &Token{
		Token:      NewToken(),
		Channel:    ic.TokenChannel(),
		Referrer:   trunc(referrer, 2048),
		InviteCode: ic.Code,
		Status:     StatusPending,
		ClientIP:   ip,
		CreatedAt:  now,
		FetchedAt:  now,
	}
	if err := CreateToken(db.DB, t); err != nil {
		return nil, err
	}
	return t, nil
}

// metadataAgentID extracts the reporting agent's id from the /report metadata.
// String only — that is what the CLI sends (cli/cmd/auth.go), and agent ids are
// snowflakes beyond float64's exact-integer range, so a JSON-number form could
// silently round to a wrong id. Returns 0 when absent/invalid.
func metadataAgentID(md map[string]any) int64 {
	s, ok := md["agent_id"].(string)
	if !ok {
		return 0
	}
	id, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return id
}

// attributeInvitedAgent writes the registration attribution for an invited
// agent: agents.invited_by_code / inviter_agent_id / invited_at, first report
// wins (the conditional UPDATE only matches while invited_by_code is empty).
//
// /install/report is public (install.sh reports pre-auth), so the identity in
// metadata is untrusted. Guards against forged attribution:
//   - the report must carry BOTH the agent_id and the account's email, and the
//     pair must match the agents row (emails are not exposed on any public
//     surface, unlike agent ids);
//   - the agent must have REGISTERED AFTER this invite entry was minted — an
//     invite can only claim signups it plausibly caused, so existing accounts
//     can never be hijacked onto someone's leaderboard;
//   - self-invites (a KOL installing through their own code) are skipped.
//
// Best effort — a failure here must never fail the install report.
func attributeInvitedAgent(t *Token, md map[string]any) {
	if t == nil || t.InviteCode == "" {
		return
	}
	agentID := metadataAgentID(md)
	email, _ := md["email"].(string)
	email = strings.ToLower(strings.TrimSpace(email))
	if agentID <= 0 || email == "" {
		return
	}
	var agent struct {
		Email     string
		CreatedAt int64
	}
	if err := db.DB.Raw(`SELECT email, created_at FROM agents WHERE agent_id = ?`, agentID).
		Scan(&agent).Error; err != nil || agent.Email == "" {
		return
	}
	if strings.ToLower(agent.Email) != email || agent.CreatedAt < t.CreatedAt {
		event("install_invite_attr_rejected", t.Token, "agent_id", agentID)
		return
	}
	ic, err := invite.GetByCode(db.DB, t.InviteCode)
	if err != nil || ic == nil {
		return
	}
	if ic.Kind == invite.KindKOL && ic.AgentID == agentID {
		return // self-invite
	}
	res := db.DB.Exec(
		`UPDATE agents SET invited_by_code = ?, inviter_agent_id = ?, invited_at = ?
		  WHERE agent_id = ? AND invited_by_code = ''`,
		ic.Code, ic.AgentID, time.Now().UnixMilli(), agentID)
	if res.Error != nil {
		logger.Default().Warn("install",
			"ev", "invite_attribute_error", "ref", t.Token, "agent_id", agentID, "err", res.Error.Error())
		return
	}
	if res.RowsAffected == 1 {
		event("install_invite_attributed", t.Token,
			"invite_code", ic.Code, "kind", ic.Kind, "agent_id", agentID, "inviter", ic.AgentID)
	}
}
