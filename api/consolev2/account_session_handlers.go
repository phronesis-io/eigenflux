package consolev2

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol"
	"gorm.io/gorm"
)

type consoleAccountView struct {
	AgentID      string `json:"agent_id"`
	AgentName    string `json:"agent_name"`
	AgentNameEn  string `json:"agent_name_en"`
	ShortID      string `json:"short_id"`
	EigenFluxID  string `json:"eigenflux_id"`
	Email        string `json:"email"`
	Slot         int    `json:"slot"`
	LastActiveAt int64  `json:"last_active_at"`
	ExpiresAt    int64  `json:"expires_at"`
	Expired      bool   `json:"expired"`
}

type consoleCookieCredential struct {
	Slot      int
	SessionID string
	Secret    string
}

func consoleSlotCookieName(base string, slot int) string {
	if slot <= 0 {
		return base
	}
	return base + "_" + strconv.Itoa(slot)
}

func consoleSessionCookieName(slot int) string { return consoleSlotCookieName(consoleCookieName, slot) }
func consoleCSRFCookieName(slot int) string    { return consoleSlotCookieName(csrfCookieName, slot) }

func activeConsoleSlot(c *app.RequestContext) int {
	if value, ok := c.Get("console_session_slot"); ok {
		if slot, valid := value.(int); valid && slot >= 0 && slot < maxConsoleAccountSlots {
			return slot
		}
	}
	slot, err := strconv.Atoi(strings.TrimSpace(string(c.Cookie(activeConsoleSlotCookieName))))
	if err != nil || slot < 0 || slot >= maxConsoleAccountSlots {
		return 0
	}
	return slot
}

func consoleCredential(c *app.RequestContext, slot int) (consoleCookieCredential, bool) {
	parts := strings.SplitN(string(c.Cookie(consoleSessionCookieName(slot))), ".", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return consoleCookieCredential{}, false
	}
	return consoleCookieCredential{Slot: slot, SessionID: parts[0], Secret: parts[1]}, true
}

func (s *Service) setConsoleCookieAtSlot(c *app.RequestContext, slot int, value string, maxAge int) {
	c.SetCookie(consoleSessionCookieName(slot), value, maxAge, "/", "", protocol.CookieSameSiteLaxMode, s.secureCookie, true)
	c.Header("Referrer-Policy", "no-referrer")
}

func (s *Service) setCSRFCookieAtSlot(c *app.RequestContext, slot int, value string, maxAge int) {
	c.SetCookie(consoleCSRFCookieName(slot), value, maxAge, "/", "", protocol.CookieSameSiteStrictMode, s.secureCookie, false)
}

func (s *Service) setActiveConsoleSlot(c *app.RequestContext, slot int, maxAge int) {
	value := ""
	if maxAge >= 0 {
		value = strconv.Itoa(slot)
	}
	c.SetCookie(activeConsoleSlotCookieName, value, maxAge, "/", "", protocol.CookieSameSiteStrictMode, s.secureCookie, false)
}

func (s *Service) loadConsoleSessionCredential(db *gorm.DB, credential consoleCookieCredential) (consoleSession, error) {
	var session consoleSession
	err := db.Raw(`SELECT s.session_id, s.agent_id, s.principal_id,
			p.agent_id AS principal_agent_id, p.status AS principal_status,
			p.revoked_at AS principal_revoked_at, a.identity_state,
			s.session_secret_hash, s.csrf_secret_hash, s.scopes,
			s.idle_expires_at, s.absolute_expires_at, s.last_seen_at,
			s.auth_method, s.recent_auth_at
		FROM console_v2_sessions s
		JOIN agent_principals p ON p.principal_id = s.principal_id
		JOIN agents a ON a.agent_id = s.agent_id
		WHERE s.session_id = ? AND s.status = 'active'`, credential.SessionID).Scan(&session).Error
	if err != nil {
		return consoleSession{}, err
	}
	if session.SessionID == "" ||
		subtle.ConstantTimeCompare([]byte(session.SecretHash), []byte(hashString(credential.Secret))) != 1 {
		return consoleSession{}, errUnauthorized
	}
	return session, nil
}

func validConsoleSessionAt(session consoleSession, now int64) bool {
	return session.SessionID != "" && session.IdleExpiresAt > now && session.AbsoluteExpiry > now &&
		validActiveIdentityBinding(session.AgentID, session.PrincipalAgentID, session.IdentityState,
			session.PrincipalStatus, session.PrincipalRevokedAt)
}

type consoleSessionStorageError struct {
	reason string
	cause  error
}

func (e *consoleSessionStorageError) Error() string { return "Console session storage is unavailable" }
func (e *consoleSessionStorageError) Unwrap() error { return e.cause }

func (s *Service) resolveActiveConsoleSession(ctx context.Context, c *app.RequestContext, now int64) (int, consoleSession, error) {
	preferred := activeConsoleSlot(c)
	order := []int{preferred}
	for slot := 0; slot < maxConsoleAccountSlots; slot++ {
		if slot != preferred {
			order = append(order, slot)
		}
	}
	foundCredential := false
	for _, slot := range order {
		credential, ok := consoleCredential(c, slot)
		if !ok {
			continue
		}
		foundCredential = true
		session, err := s.loadConsoleSessionCredential(s.authenticationDB(ctx), credential)
		if err != nil {
			if errors.Is(err, errUnauthorized) {
				continue
			}
			return 0, consoleSession{}, &consoleSessionStorageError{reason: "session_lookup_failed", cause: err}
		}
		if !validConsoleSessionAt(session, now) {
			if !validActiveIdentityBinding(session.AgentID, session.PrincipalAgentID, session.IdentityState,
				session.PrincipalStatus, session.PrincipalRevokedAt) {
				if err := s.authenticationDB(ctx).Exec(`UPDATE console_v2_sessions SET status = 'revoked', revoked_at = ?
					WHERE session_id = ? AND status = 'active'`, now, session.SessionID).Error; err != nil {
					return 0, session, &consoleSessionStorageError{reason: "session_revocation_failed", cause: err}
				}
			}
			continue
		}
		if slot != preferred {
			s.setActiveConsoleSlot(c, slot, int(consoleAbsoluteTTL/time.Second))
		}
		return slot, session, nil
	}
	if foundCredential {
		return 0, consoleSession{}, errConsoleSessionInvalid
	}
	return 0, consoleSession{}, errUnauthorized
}

func (s *Service) consoleAccountView(db *gorm.DB, slot int, session consoleSession, now int64) (consoleAccountView, error) {
	var identity struct {
		AgentName   string `gorm:"column:agent_name"`
		AgentNameEn string `gorm:"column:agent_name_en"`
		ShortID     string `gorm:"column:short_id"`
		Email       string `gorm:"column:email"`
	}
	err := db.Raw(`SELECT a.agent_name, a.agent_name_en, a.short_id,
		COALESCE((SELECT binding.normalized_email FROM agent_email_bindings binding
			WHERE binding.agent_id = a.agent_id AND binding.status = 'active'
				AND binding.verification_state = 'verified'
			ORDER BY binding.updated_at DESC, binding.binding_id DESC LIMIT 1), '') AS email
		FROM agents a
		WHERE a.agent_id = ?`, session.AgentID).Scan(&identity).Error
	if err != nil {
		return consoleAccountView{}, err
	}
	expiresAt := session.IdleExpiresAt
	if session.AbsoluteExpiry < expiresAt {
		expiresAt = session.AbsoluteExpiry
	}
	return consoleAccountView{
		AgentID: strconv.FormatInt(session.AgentID, 10), AgentName: identity.AgentName,
		AgentNameEn: identity.AgentNameEn, ShortID: identity.ShortID,
		EigenFluxID: "eigenflux#" + identity.ShortID, Email: identity.Email, Slot: slot,
		LastActiveAt: session.LastSeenAt, ExpiresAt: expiresAt,
		Expired: !validConsoleSessionAt(session, now),
	}, nil
}

func (s *Service) consoleAccounts(db *gorm.DB, c *app.RequestContext, now int64) []consoleAccountView {
	accounts := make([]consoleAccountView, 0, maxConsoleAccountSlots)
	for slot := 0; slot < maxConsoleAccountSlots; slot++ {
		credential, ok := consoleCredential(c, slot)
		if !ok {
			continue
		}
		session, err := s.loadConsoleSessionCredential(db, credential)
		if err != nil {
			continue
		}
		account, err := s.consoleAccountView(db, slot, session, now)
		if err == nil {
			accounts = append(accounts, account)
		}
	}
	return uniqueConsoleAccounts(accounts, activeConsoleSlot(c))
}

func uniqueConsoleAccounts(accounts []consoleAccountView, activeSlot int) []consoleAccountView {
	unique := make([]consoleAccountView, 0, len(accounts))
	indices := make(map[string]int, len(accounts))
	for _, account := range accounts {
		if index, exists := indices[account.AgentID]; exists {
			current := unique[index]
			prefer := current.Expired && !account.Expired
			if current.Expired == account.Expired {
				prefer = account.Slot == activeSlot || (current.Slot != activeSlot && account.LastActiveAt > current.LastActiveAt)
			}
			if prefer {
				unique[index] = account
			}
			continue
		}
		indices[account.AgentID] = len(unique)
		unique = append(unique, account)
	}
	sort.SliceStable(unique, func(i, j int) bool { return unique[i].LastActiveAt > unique[j].LastActiveAt })
	return unique
}

func (s *Service) chooseEmailLoginSessionSlot(tx *gorm.DB, c *app.RequestContext, targetAgentID int64) (int, string) {
	// A normal login refreshes an existing browser identity even when it lives
	// outside slot zero. New identities retain the slot-zero replacement rule.
	selectedSlot := 0
	replacedSessionID := ""
	for slot := 0; slot < maxConsoleAccountSlots; slot++ {
		credential, ok := consoleCredential(c, slot)
		if !ok {
			continue
		}
		session, err := s.loadConsoleSessionCredential(tx, credential)
		if err != nil {
			continue
		}
		if slot == 0 || session.AgentID == targetAgentID {
			selectedSlot, replacedSessionID = slot, session.SessionID
		}
		if session.AgentID == targetAgentID {
			break
		}
	}
	return selectedSlot, replacedSessionID
}

func (s *Service) revokeConsoleAccountSessions(c *app.RequestContext, targetAgentID, now int64) ([]int, error) {
	slots := make([]int, 0, maxConsoleAccountSlots)
	err := s.db.Transaction(func(tx *gorm.DB) error {
		sessionIDs := make([]string, 0, maxConsoleAccountSlots)
		for slot := 0; slot < maxConsoleAccountSlots; slot++ {
			credential, ok := consoleCredential(c, slot)
			if !ok {
				continue
			}
			session, loadErr := s.loadConsoleSessionCredential(tx, credential)
			if loadErr != nil || session.AgentID != targetAgentID {
				continue
			}
			slots = append(slots, slot)
			sessionIDs = append(sessionIDs, session.SessionID)
		}
		if len(sessionIDs) > 0 {
			return tx.Exec(`UPDATE console_v2_sessions SET status = 'revoked', revoked_at = ?
				WHERE session_id IN ? AND status = 'active'`, now, sessionIDs).Error
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, slot := range slots {
		s.setConsoleCookieAtSlot(c, slot, "", -1)
		s.setCSRFCookieAtSlot(c, slot, "", -1)
	}
	return slots, nil
}

func parseReplacementAgentID(value string) (int64, error) {
	if strings.TrimSpace(value) == "" {
		return 0, nil
	}
	id, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("invalid replacement Agent")
	}
	return id, nil
}

func (s *Service) chooseConsoleSessionSlot(tx *gorm.DB, c *app.RequestContext, targetAgentID, replacementAgentID int64, now int64) (int, string, []consoleAccountView, error) {
	freeSlot := -1
	targetSlot, replacementSlot, duplicateSlot := -1, -1, -1
	targetSessionID, replacementSessionID, duplicateSessionID := "", "", ""
	seenAgents := make(map[int64]string, maxConsoleAccountSlots)
	accounts := make([]consoleAccountView, 0, maxConsoleAccountSlots)
	for slot := 0; slot < maxConsoleAccountSlots; slot++ {
		credential, ok := consoleCredential(c, slot)
		if !ok {
			if freeSlot < 0 {
				freeSlot = slot
			}
			continue
		}
		session, err := s.loadConsoleSessionCredential(tx, credential)
		if err != nil || !validConsoleSessionAt(session, now) {
			if freeSlot < 0 {
				freeSlot = slot
			}
			continue
		}
		account, accountErr := s.consoleAccountView(tx, slot, session, now)
		if accountErr == nil {
			accounts = append(accounts, account)
		}
		if session.AgentID == targetAgentID && (targetSlot < 0 || slot == activeConsoleSlot(c)) {
			targetSlot, targetSessionID = slot, session.SessionID
		}
		if replacementAgentID > 0 && session.AgentID == replacementAgentID {
			replacementSlot, replacementSessionID = slot, session.SessionID
		}
		if existingSessionID, exists := seenAgents[session.AgentID]; exists && duplicateSlot < 0 {
			duplicateSlot = slot
			if existingSessionID != session.SessionID {
				duplicateSessionID = session.SessionID
			}
		}
		seenAgents[session.AgentID] = session.SessionID
	}
	accounts = uniqueConsoleAccounts(accounts, activeConsoleSlot(c))
	if targetSlot >= 0 {
		return targetSlot, targetSessionID, accounts, nil
	}
	if replacementAgentID > 0 {
		if replacementSlot >= 0 {
			return replacementSlot, replacementSessionID, accounts, nil
		}
		return 0, "", accounts, errConflict
	}
	if freeSlot >= 0 {
		return freeSlot, "", accounts, nil
	}
	if duplicateSlot >= 0 {
		return duplicateSlot, duplicateSessionID, accounts, nil
	}
	return 0, "", accounts, errConsoleAccountLimit
}

func (s *Service) listConsoleAccounts(_ context.Context, c *app.RequestContext) {
	now := time.Now().UnixMilli()
	accounts := s.consoleAccounts(s.db, c, now)
	activeID, _ := agentID(c)
	reply(c, http.StatusOK, map[string]interface{}{
		"accounts": accounts, "active_agent_id": strconv.FormatInt(activeID, 10),
		"active_slot": activeConsoleSlot(c), "max_accounts": maxConsoleAccountSlots,
	})
}

func (s *Service) activateConsoleAccount(_ context.Context, c *app.RequestContext) {
	targetID, err := strconv.ParseInt(strings.TrimSpace(c.Param("agent_id")), 10, 64)
	if err != nil || targetID <= 0 {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "a valid agent_id is required", nil)
		return
	}
	now := time.Now().UnixMilli()
	selectedSlot := -1
	var selectedSession consoleSession
	for slot := 0; slot < maxConsoleAccountSlots; slot++ {
		credential, ok := consoleCredential(c, slot)
		if !ok {
			continue
		}
		session, loadErr := s.loadConsoleSessionCredential(s.db, credential)
		if loadErr == nil && validConsoleSessionAt(session, now) && session.AgentID == targetID {
			if selectedSlot < 0 || slot == activeConsoleSlot(c) ||
				(selectedSlot != activeConsoleSlot(c) && session.LastSeenAt > selectedSession.LastSeenAt) {
				selectedSlot, selectedSession = slot, session
			}
		}
	}
	if selectedSlot >= 0 {
		s.setActiveConsoleSlot(c, selectedSlot, int(consoleAbsoluteTTL/time.Second))
		reply(c, http.StatusOK, map[string]interface{}{"activated": true, "agent_id": c.Param("agent_id"), "slot": selectedSlot})
		return
	}
	fail(c, http.StatusNotFound, "CONSOLE_ACCOUNT_NOT_FOUND", "this account is not available in the browser", nil)
}

func (s *Service) removeConsoleAccount(_ context.Context, c *app.RequestContext) {
	targetID, err := strconv.ParseInt(strings.TrimSpace(c.Param("agent_id")), 10, 64)
	if err != nil || targetID <= 0 {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "a valid agent_id is required", nil)
		return
	}
	now := time.Now().UnixMilli()
	currentAgentID, _ := agentID(c)
	removedSlot := -1
	removedSlots, err := s.revokeConsoleAccountSessions(c, targetID, now)
	if err != nil {
		fail(c, http.StatusInternalServerError, "LOGOUT_FAILED", "could not revoke Console V2 session", nil)
		return
	}
	for _, slot := range removedSlots {
		if removedSlot < 0 || slot == activeConsoleSlot(c) {
			removedSlot = slot
		}
	}
	if removedSlot < 0 {
		fail(c, http.StatusNotFound, "CONSOLE_ACCOUNT_NOT_FOUND", "this account is not available in the browser", nil)
		return
	}
	activeSlot := activeConsoleSlot(c)
	nextAgentID := int64(0)
	if activeSlot == removedSlot {
		nextSlot := -1
		for slot := 0; slot < maxConsoleAccountSlots; slot++ {
			if slot == removedSlot {
				continue
			}
			credential, ok := consoleCredential(c, slot)
			if !ok {
				continue
			}
			session, loadErr := s.loadConsoleSessionCredential(s.db, credential)
			if loadErr == nil && validConsoleSessionAt(session, now) {
				nextSlot = slot
				nextAgentID = session.AgentID
				break
			}
		}
		if nextSlot >= 0 {
			s.setActiveConsoleSlot(c, nextSlot, int(consoleAbsoluteTTL/time.Second))
		} else {
			s.setActiveConsoleSlot(c, 0, -1)
		}
	}
	reply(c, http.StatusOK, map[string]interface{}{
		"removed": true, "agent_id": c.Param("agent_id"),
		"active_agent_id": func() string {
			if activeSlot != removedSlot {
				return strconv.FormatInt(currentAgentID, 10)
			}
			if nextAgentID > 0 {
				return strconv.FormatInt(nextAgentID, 10)
			}
			return ""
		}(),
	})
}

func consoleAccountLimitDetails(accounts []consoleAccountView, incomingAgentID int64) map[string]interface{} {
	return map[string]interface{}{
		"max_accounts":      maxConsoleAccountSlots,
		"accounts":          accounts,
		"incoming_agent_id": fmt.Sprintf("%d", incomingAgentID),
	}
}
