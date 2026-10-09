package consolev2

import (
	"crypto/subtle"

	"github.com/cloudwego/hertz/pkg/app"
	"gorm.io/gorm"
)

// Resume only the exact session created by this ticket, including inactive slots.
func (s *Service) resumeHandoffSession(tx *gorm.DB, c *app.RequestContext, sessionID string, agentID, principalID, now int64, accountSwitch bool) (int, string, int, error) {
	if sessionID == "" {
		return 0, "", 0, errUnauthorized
	}
	for slot := 0; slot < maxConsoleAccountSlots; slot++ {
		credential, ok := consoleCredential(c, slot)
		if !ok || credential.SessionID != sessionID {
			continue
		}
		session, err := s.loadConsoleSessionCredential(tx, credential)
		if err != nil || !validConsoleSessionAt(session, now) || session.AgentID != agentID || session.PrincipalID != principalID {
			return 0, "", 0, errUnauthorized
		}
		csrf := string(c.Cookie(consoleCSRFCookieName(slot)))
		if csrf == "" || subtle.ConstantTimeCompare([]byte(hashString(csrf)), []byte(session.CSRFSecretHash)) != 1 {
			return 0, "", 0, errUnauthorized
		}
		if accountSwitch {
			record, err := loadCLIAccountSwitch(tx, cliAccountSwitchToken(c), false)
			if err != nil || record.SourceConsoleSession != sessionID || record.PrincipalID != principalID ||
				record.ExpiresAt <= now || (record.Status != "pending_target" && record.Status != "pending_onboarding" && record.Status != "completed_noop") {
				return 0, "", 0, errUnauthorized
			}
		}
		return slot, csrf, int((session.AbsoluteExpiry - now) / 1000), nil
	}
	return 0, "", 0, errUnauthorized
}
