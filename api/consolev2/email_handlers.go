package consolev2

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

const emailChallengeTTL = 10 * time.Minute

var (
	v2EmailPattern = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)
	errRateLimited = errors.New("rate limited")
	errInvalidOTP  = errors.New("invalid otp")
)

type emailJob struct {
	challengeID string
	to          string
	otp         string
}

func (s *Service) startEmailWorkers(workerCount, queueSize int) {
	if s.emailSender == nil || workerCount <= 0 || queueSize <= 0 {
		return
	}
	s.emailQueue = make(chan emailJob, queueSize)
	for i := 0; i < workerCount; i++ {
		go func() {
			for job := range s.emailQueue {
				// A no-op job gives public login challenges the same HTTP queueing
				// behavior whether or not the email is bound to an Agent.
				if job.to == "" {
					continue
				}
				if err := s.emailSender.SendLoginVerifyMail(context.Background(), job.to, job.otp); err != nil {
					_ = s.db.Exec(`UPDATE v2_email_challenges SET status = 'revoked'
						WHERE challenge_id = ? AND status = 'pending'`, job.challengeID).Error
				}
			}
		}()
	}
}

func normalizeV2Email(raw string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	if len(normalized) == 0 || len(normalized) > 254 || !v2EmailPattern.MatchString(normalized) {
		return "", errors.New("invalid email")
	}
	return normalized, nil
}

func generateV2OTP() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

func (s *Service) otpDigest(challengeID, purpose, normalizedEmail, otp string) string {
	return keyedHash(s.otpPepper, strings.Join([]string{challengeID, purpose, normalizedEmail, otp}, "\x00"))
}

func (s *Service) clientIPHash(c *app.RequestContext) string {
	remote := "unknown"
	if c.RemoteAddr() != nil && c.RemoteAddr().String() != "" {
		remote = c.RemoteAddr().String()
	}
	return keyedHash(s.otpPepper, remote)
}

type createEmailChallengeRequest struct {
	Email   string `json:"email"`
	Purpose string `json:"purpose,omitempty"`
}

type emailChallengeRow struct {
	ChallengeID         string  `gorm:"column:challenge_id"`
	Purpose             string  `gorm:"column:purpose"`
	NormalizedEmailHash string  `gorm:"column:normalized_email_hash"`
	SubjectAgentID      *int64  `gorm:"column:subject_agent_id"`
	ConsoleSessionID    *string `gorm:"column:console_session_id"`
	OTPHMAC             string  `gorm:"column:otp_hmac"`
	Status              string  `gorm:"column:status"`
	AttemptCount        int     `gorm:"column:attempt_count"`
	MaxAttempts         int     `gorm:"column:max_attempts"`
	ExpiresAt           int64   `gorm:"column:expires_at"`
}

func (s *Service) insertEmailChallenge(tx *gorm.DB, normalizedEmail, purpose string, subjectAgentID *int64, sessionID *string, clientIPHash string, now int64) (emailJob, int64, error) {
	emailHash := keyedHash(s.otpPepper, normalizedEmail)
	windowStart := now - int64(10*time.Minute/time.Millisecond)
	var emailCount, ipCount, sessionCount, globalCount int64
	if err := tx.Raw(`SELECT COUNT(*) FROM v2_email_challenges
		WHERE normalized_email_hash = ? AND created_at >= ?`, emailHash, windowStart).Scan(&emailCount).Error; err != nil {
		return emailJob{}, 0, err
	}
	if err := tx.Raw(`SELECT COUNT(*) FROM v2_email_challenges
		WHERE client_ip_hash = ? AND created_at >= ?`, clientIPHash, windowStart).Scan(&ipCount).Error; err != nil {
		return emailJob{}, 0, err
	}
	if sessionID != nil {
		if err := tx.Raw(`SELECT COUNT(*) FROM v2_email_challenges
			WHERE console_session_id = ? AND created_at >= ?`, *sessionID, windowStart).Scan(&sessionCount).Error; err != nil {
			return emailJob{}, 0, err
		}
	}
	if err := tx.Raw(`SELECT COUNT(*) FROM v2_email_challenges WHERE created_at >= ?`, windowStart).Scan(&globalCount).Error; err != nil {
		return emailJob{}, 0, err
	}
	if emailCount >= 5 || ipCount >= 20 || sessionCount >= 5 || globalCount >= 1000 {
		return emailJob{}, 0, errRateLimited
	}
	challengeID, err := randomToken("efec_", 18)
	if err != nil {
		return emailJob{}, 0, err
	}
	otp, err := generateV2OTP()
	if err != nil {
		return emailJob{}, 0, err
	}
	expiresAt := now + int64(emailChallengeTTL/time.Millisecond)
	if err := tx.Exec(`INSERT INTO v2_email_challenges
		(challenge_id, purpose, normalized_email_hash, subject_agent_id, console_session_id,
		 otp_hmac, status, attempt_count, max_attempts, expires_at, client_ip_hash, created_at)
		VALUES (?, ?, ?, ?, ?, ?, 'pending', 0, 5, ?, ?, ?)`, challengeID, purpose, emailHash,
		subjectAgentID, sessionID, s.otpDigest(challengeID, purpose, normalizedEmail, otp), expiresAt,
		clientIPHash, now).Error; err != nil {
		return emailJob{}, 0, err
	}
	return emailJob{challengeID: challengeID, to: normalizedEmail, otp: otp}, expiresAt, nil
}

func (s *Service) queueEmailChallenge(c *app.RequestContext, job emailJob, expiresAt int64) {
	if s.emailQueue == nil {
		_ = s.db.Exec(`UPDATE v2_email_challenges SET status = 'revoked'
			WHERE challenge_id = ? AND status = 'pending'`, job.challengeID).Error
		fail(c, http.StatusServiceUnavailable, "EMAIL_DELIVERY_UNAVAILABLE", "email verification is temporarily unavailable", nil)
		return
	}
	select {
	case s.emailQueue <- job:
		reply(c, http.StatusAccepted, map[string]interface{}{
			"accepted": true, "challenge_id": job.challengeID, "expires_at": expiresAt,
		})
	default:
		_ = s.db.Exec(`UPDATE v2_email_challenges SET status = 'revoked'
			WHERE challenge_id = ? AND status = 'pending'`, job.challengeID).Error
		fail(c, http.StatusServiceUnavailable, "EMAIL_DELIVERY_BUSY", "email verification is temporarily busy", nil)
	}
}

func (s *Service) createEmailBindingChallenge(_ context.Context, c *app.RequestContext) {
	agentIDValue, ok := agentID(c)
	sessionValue, hasSession := c.Get("console_session_id")
	sessionID, sessionOK := sessionValue.(string)
	if !ok || !hasSession || !sessionOK {
		fail(c, http.StatusUnauthorized, "CONSOLE_SESSION_REQUIRED", "Console V2 session is required", nil)
		return
	}
	var req createEmailChallengeRequest
	if err := decodeBody(c, &req); err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "a valid email is required", nil)
		return
	}
	normalizedEmail, err := normalizeV2Email(req.Email)
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "a valid email is required", nil)
		return
	}
	now := time.Now().UnixMilli()
	var job emailJob
	var expiresAt int64
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?, 0))`, keyedHash(s.otpPepper, normalizedEmail)).Error; err != nil {
			return err
		}
		var createErr error
		job, expiresAt, createErr = s.insertEmailChallenge(tx, normalizedEmail, "bind", &agentIDValue, &sessionID, s.clientIPHash(c), now)
		return createErr
	})
	if errors.Is(err, errRateLimited) {
		fail(c, http.StatusTooManyRequests, "EMAIL_RATE_LIMITED", "too many verification attempts; try again later", nil)
		return
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, "EMAIL_CHALLENGE_FAILED", "could not create email verification challenge", nil)
		return
	}
	s.queueEmailChallenge(c, job, expiresAt)
}

func (s *Service) createEmailLoginChallenge(_ context.Context, c *app.RequestContext) {
	if s.emailQueue == nil {
		fail(c, http.StatusServiceUnavailable, "EMAIL_DELIVERY_UNAVAILABLE", "email verification is temporarily unavailable", nil)
		return
	}
	var req createEmailChallengeRequest
	if err := decodeBody(c, &req); err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "a valid email is required", nil)
		return
	}
	if req.Purpose == "" {
		req.Purpose = "login"
	}
	if req.Purpose != "login" && req.Purpose != "recovery" {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "purpose must be login or recovery", nil)
		return
	}
	normalizedEmail, err := normalizeV2Email(req.Email)
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "a valid email is required", nil)
		return
	}
	now := time.Now().UnixMilli()
	var job emailJob
	var expiresAt int64
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?, 0))`, keyedHash(s.otpPepper, normalizedEmail)).Error; err != nil {
			return err
		}
		var binding struct {
			AgentID int64 `gorm:"column:agent_id"`
		}
		if err := tx.Raw(`SELECT agent_id FROM agent_email_bindings
			WHERE normalized_email = ? AND status = 'active' AND verification_state = 'verified'`, normalizedEmail).Scan(&binding).Error; err != nil {
			return err
		}
		var subject *int64
		if binding.AgentID != 0 {
			subject = &binding.AgentID
		}
		var createErr error
		job, expiresAt, createErr = s.insertEmailChallenge(tx, normalizedEmail, req.Purpose, subject, nil, s.clientIPHash(c), now)
		if subject == nil {
			job.to = ""
		}
		return createErr
	})
	if errors.Is(err, errRateLimited) {
		fail(c, http.StatusTooManyRequests, "EMAIL_RATE_LIMITED", "too many verification attempts; try again later", nil)
		return
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, "EMAIL_CHALLENGE_FAILED", "could not create email verification challenge", nil)
		return
	}
	s.queueEmailChallenge(c, job, expiresAt)
}

type verifyEmailRequest struct {
	ChallengeID string `json:"challenge_id"`
	Email       string `json:"email"`
	OTP         string `json:"otp"`
	Purpose     string `json:"purpose,omitempty"`
}

func (s *Service) lockAndCheckEmailChallenge(tx *gorm.DB, req verifyEmailRequest, normalizedEmail, purpose string, subjectAgentID *int64, sessionID *string, now int64) (emailChallengeRow, bool, error) {
	var row emailChallengeRow
	if err := tx.Raw(`SELECT challenge_id, purpose, normalized_email_hash, subject_agent_id,
		console_session_id, otp_hmac, status, attempt_count, max_attempts, expires_at
		FROM v2_email_challenges WHERE challenge_id = ? FOR UPDATE`, req.ChallengeID).Scan(&row).Error; err != nil {
		return row, false, err
	}
	if row.ChallengeID == "" || row.Purpose != purpose || row.NormalizedEmailHash != keyedHash(s.otpPepper, normalizedEmail) ||
		row.Status != "pending" || row.ExpiresAt < now || !sameOptionalInt64(row.SubjectAgentID, subjectAgentID) ||
		!sameOptionalString(row.ConsoleSessionID, sessionID) {
		return row, false, errUnauthorized
	}
	expected := s.otpDigest(row.ChallengeID, purpose, normalizedEmail, req.OTP)
	if subtle.ConstantTimeCompare([]byte(expected), []byte(row.OTPHMAC)) != 1 {
		newAttempts := row.AttemptCount + 1
		status := "pending"
		if newAttempts >= row.MaxAttempts {
			status = "revoked"
		}
		if err := tx.Exec(`UPDATE v2_email_challenges SET attempt_count = ?, status = ?
			WHERE challenge_id = ? AND status = 'pending'`, newAttempts, status, row.ChallengeID).Error; err != nil {
			return row, false, err
		}
		return row, false, nil
	}
	return row, true, nil
}

func sameOptionalInt64(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func sameOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func (s *Service) verifyEmailBinding(_ context.Context, c *app.RequestContext) {
	agentIDValue, ok := agentID(c)
	sessionValue, hasSession := c.Get("console_session_id")
	sessionID, sessionOK := sessionValue.(string)
	if !ok || !hasSession || !sessionOK {
		fail(c, http.StatusUnauthorized, "CONSOLE_SESSION_REQUIRED", "Console V2 session is required", nil)
		return
	}
	var req verifyEmailRequest
	if err := decodeBody(c, &req); err != nil || req.ChallengeID == "" || req.OTP == "" {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "challenge_id, email, and otp are required", nil)
		return
	}
	normalizedEmail, err := normalizeV2Email(req.Email)
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "challenge_id, email, and otp are required", nil)
		return
	}
	now := time.Now().UnixMilli()
	validOTP := false
	err = s.db.Transaction(func(tx *gorm.DB) error {
		_, valid, checkErr := s.lockAndCheckEmailChallenge(tx, req, normalizedEmail, "bind", &agentIDValue, &sessionID, now)
		if checkErr != nil || !valid {
			validOTP = false
			return checkErr
		}
		validOTP = true
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?, 0))`, keyedHash(s.otpPepper, normalizedEmail)).Error; err != nil {
			return err
		}
		var bindingOwners []struct {
			AgentID int64 `gorm:"column:agent_id"`
		}
		if err := tx.Raw(`SELECT agent_id FROM agent_email_bindings
			WHERE normalized_email = ? AND status = 'active'`, normalizedEmail).Scan(&bindingOwners).Error; err != nil {
			return err
		}
		for _, owner := range bindingOwners {
			if owner.AgentID != agentIDValue {
				return errConflict
			}
		}
		var legacyOwners []struct {
			AgentID int64 `gorm:"column:agent_id"`
		}
		if err := tx.Raw(`SELECT agent_id FROM agents
			WHERE lower(btrim(email)) = ? AND email_kind = 'legacy_real' ORDER BY agent_id LIMIT 2`, normalizedEmail).Scan(&legacyOwners).Error; err != nil {
			return err
		}
		for _, owner := range legacyOwners {
			if owner.AgentID != agentIDValue {
				return errConflict
			}
		}
		var current struct {
			BindingID       int64  `gorm:"column:binding_id"`
			NormalizedEmail string `gorm:"column:normalized_email"`
		}
		if err := tx.Raw(`SELECT binding_id, normalized_email FROM agent_email_bindings
			WHERE agent_id = ? AND status = 'active' FOR UPDATE`, agentIDValue).Scan(&current).Error; err != nil {
			return err
		}
		if current.BindingID != 0 && current.NormalizedEmail != normalizedEmail {
			return errConflict
		}
		if current.BindingID == 0 {
			if err := tx.Exec(`INSERT INTO agent_email_bindings
				(agent_id, normalized_email, normalization_version, verification_state, status,
				 verified_at, created_at, updated_at)
				VALUES (?, ?, 1, 'verified', 'active', ?, ?, ?)`, agentIDValue, normalizedEmail, now, now, now).Error; err != nil {
				return err
			}
		} else if err := tx.Exec(`UPDATE agent_email_bindings
			SET verification_state = 'verified', verified_at = ?, updated_at = ?
			WHERE binding_id = ?`, now, now, current.BindingID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`UPDATE agents SET email = ?, email_kind = 'v2_bound', email_verified_at = ?, updated_at = ?
			WHERE agent_id = ?`, normalizedEmail, now, now, agentIDValue).Error; err != nil {
			return err
		}
		return tx.Exec(`UPDATE v2_email_challenges SET status = 'consumed', consumed_at = ?
			WHERE challenge_id = ? AND status = 'pending'`, now, req.ChallengeID).Error
	})
	if errors.Is(err, errUnauthorized) || (!validOTP && err == nil) {
		fail(c, http.StatusUnauthorized, "OTP_INVALID", "verification code is invalid or expired", nil)
		return
	}
	if errors.Is(err, errConflict) || isUniqueViolation(err) {
		fail(c, http.StatusConflict, "EMAIL_UNAVAILABLE", "this email cannot be used for the requested operation", nil)
		return
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, "EMAIL_BIND_FAILED", "could not bind email", nil)
		return
	}
	reply(c, http.StatusOK, map[string]interface{}{
		"bound": true, "verification_level": "email_verified",
	})
}

func isUniqueViolation(err error) bool {
	var pgErr *pq.Error
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func (s *Service) verifyEmailLogin(_ context.Context, c *app.RequestContext) {
	var req verifyEmailRequest
	if err := decodeBody(c, &req); err != nil || req.ChallengeID == "" || req.OTP == "" {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "challenge_id, email, otp, and purpose are required", nil)
		return
	}
	if req.Purpose == "" {
		req.Purpose = "login"
	}
	if req.Purpose != "login" && req.Purpose != "recovery" {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "purpose must be login or recovery", nil)
		return
	}
	normalizedEmail, err := normalizeV2Email(req.Email)
	if err != nil {
		fail(c, http.StatusBadRequest, "INVALID_REQUEST", "challenge_id, email, otp, and purpose are required", nil)
		return
	}
	sessionID, _ := randomToken("efcs_", 18)
	sessionSecret, _ := randomToken("", 32)
	csrfSecret, _ := randomToken("efcsrf_", 24)
	now := time.Now().UnixMilli()
	validOTP := false
	var recoveredAgentID int64
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var challenge emailChallengeRow
		if err := tx.Raw(`SELECT challenge_id, purpose, normalized_email_hash, subject_agent_id,
			console_session_id, otp_hmac, status, attempt_count, max_attempts, expires_at
			FROM v2_email_challenges WHERE challenge_id = ? FOR UPDATE`, req.ChallengeID).Scan(&challenge).Error; err != nil {
			return err
		}
		if challenge.SubjectAgentID == nil {
			return errUnauthorized
		}
		checked, valid, checkErr := s.lockAndCheckEmailChallenge(tx, req, normalizedEmail, req.Purpose, challenge.SubjectAgentID, nil, now)
		if checkErr != nil || !valid {
			validOTP = false
			return checkErr
		}
		validOTP = true
		recoveredAgentID = *checked.SubjectAgentID
		var principal struct {
			PrincipalID int64 `gorm:"column:principal_id"`
		}
		if err := tx.Raw(`SELECT principal_id FROM agent_principals
			WHERE agent_id = ? AND revoked_at IS NULL AND status IN ('limited','active')
			ORDER BY principal_id LIMIT 1`, recoveredAgentID).Scan(&principal).Error; err != nil {
			return err
		}
		if principal.PrincipalID == 0 {
			return errUnauthorized
		}
		consume := tx.Exec(`UPDATE v2_email_challenges SET status = 'consumed', consumed_at = ?
			WHERE challenge_id = ? AND status = 'pending'`, now, req.ChallengeID)
		if consume.Error != nil || consume.RowsAffected != 1 {
			return errUnauthorized
		}
		return tx.Exec(`INSERT INTO console_v2_sessions
			(session_id, session_secret_hash, agent_id, principal_id, csrf_secret_hash,
			 status, scopes, issued_at, idle_expires_at, absolute_expires_at, last_seen_at)
			VALUES (?, ?, ?, ?, ?, 'active', ?, ?, ?, ?, ?)`, sessionID, hashString(sessionSecret),
			recoveredAgentID, principal.PrincipalID, hashString(csrfSecret),
			pq.Array([]string{"console:onboarding", "console:read", "console:write"}), now,
			now+int64(30*time.Minute/time.Millisecond), now+int64(12*time.Hour/time.Millisecond), now).Error
	})
	if errors.Is(err, errUnauthorized) || (!validOTP && err == nil) {
		fail(c, http.StatusUnauthorized, "OTP_INVALID", "verification code is invalid or expired", nil)
		return
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, "EMAIL_LOGIN_FAILED", "could not establish Console V2 session", nil)
		return
	}
	s.setConsoleCookie(c, sessionID+"."+sessionSecret, int((12*time.Hour)/time.Second))
	s.setCSRFCookie(c, csrfSecret, int((12*time.Hour)/time.Second))
	reply(c, http.StatusOK, map[string]interface{}{
		"agent_id": fmt.Sprintf("%d", recoveredAgentID), "csrf_token": csrfSecret,
	})
}
