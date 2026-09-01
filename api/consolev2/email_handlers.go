package consolev2

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/lib/pq"
	redis "github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

const emailChallengeTTL = 10 * time.Minute

var (
	v2EmailPattern = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)
	errRateLimited = errors.New("rate limited")
	errInvalidOTP  = errors.New("invalid otp")
)

type emailJob struct {
	challengeID  string
	to           string
	otp          string
	skipDelivery bool
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
				sendContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				err := s.emailSender.SendLoginVerifyMail(sendContext, job.to, job.otp)
				cancel()
				if err != nil {
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

func (s *Service) generateChallengeOTP(normalizedEmail string) (string, bool, error) {
	if otp, matched := s.fixedTestOTP(normalizedEmail); matched {
		return otp, true, nil
	}
	otp, err := generateV2OTP()
	return otp, false, err
}

func (s *Service) clientIPHash(c *app.RequestContext) string {
	remote := resolveV2ClientIP(c.RemoteAddr().String(),
		string(c.Request.Header.Peek("X-Forwarded-For")),
		string(c.Request.Header.Peek("X-Real-IP")), s.trustedProxyNets)
	return keyedHash(s.otpPepper, remote)
}

func resolveV2ClientIP(remoteAddr, forwardedFor, realIP string, trustedProxyNets []*net.IPNet) string {
	remote := strings.TrimSpace(remoteAddr)
	if host, _, err := net.SplitHostPort(remote); err == nil {
		remote = host
	}
	remoteIP := net.ParseIP(remote)
	trustedProxy := false
	for _, network := range trustedProxyNets {
		if remoteIP != nil && network.Contains(remoteIP) {
			trustedProxy = true
			break
		}
	}
	if trustedProxy {
		forwarded := strings.TrimSpace(forwardedFor)
		if comma := strings.IndexByte(forwarded, ','); comma >= 0 {
			forwarded = strings.TrimSpace(forwarded[:comma])
		}
		if net.ParseIP(forwarded) == nil {
			forwarded = strings.TrimSpace(realIP)
		}
		if net.ParseIP(forwarded) != nil {
			remote = forwarded
		}
	}
	if net.ParseIP(remote) == nil {
		remote = "unknown"
	}
	return remote
}

var emailChallengeRateScript = redis.NewScript(`
for i, key in ipairs(KEYS) do
  local count = redis.call('INCR', key)
  if count == 1 then redis.call('PEXPIRE', key, ARGV[1]) end
  if count > tonumber(ARGV[i + 1]) then return 0 end
end
return 1
`)

func (s *Service) allowEmailChallenge(ctx context.Context, emailHash, clientIPHash string, sessionID *string) (bool, error) {
	if s.redisClient == nil {
		return false, errors.New("redis client is unavailable")
	}
	keys := []string{"console:v2:otp:email:" + emailHash, "console:v2:otp:ip:" + clientIPHash}
	limits := []interface{}{int64(10 * time.Minute / time.Millisecond), int64(5), int64(20)}
	if sessionID != nil {
		keys = append(keys, "console:v2:otp:session:"+hashString(*sessionID))
		limits = append(limits, int64(5))
	}
	keys = append(keys, "console:v2:otp:global")
	limits = append(limits, int64(1000))
	allowed, err := emailChallengeRateScript.Run(ctx, s.redisClient, keys, limits...).Int()
	return allowed == 1, err
}

func (s *Service) preflightEmailChallengeRate(ctx context.Context, normalizedEmail, clientIPHash string, sessionID *string) (bool, error) {
	if s.redisClient == nil {
		return false, nil
	}
	rateContext, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	allowed, err := s.allowEmailChallenge(rateContext, keyedHash(s.otpPepper, normalizedEmail), clientIPHash, sessionID)
	if err != nil {
		return true, err
	}
	if !allowed {
		return true, errRateLimited
	}
	return true, nil
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

func (s *Service) insertEmailChallenge(tx *gorm.DB, normalizedEmail, purpose string, subjectAgentID *int64, sessionID *string, clientIPHash string, now int64, rateChecked bool) (emailJob, int64, error) {
	emailHash := keyedHash(s.otpPepper, normalizedEmail)
	if !rateChecked {
		// Integration/dev fallback. Production config wires the shared Redis
		// client, avoiding a global database lock on every email request.
		if err := tx.Exec(`SELECT
			pg_advisory_xact_lock(hashtextextended('console-v2-otp-global', 0)),
			pg_advisory_xact_lock(hashtextextended(?, 0))`, "console-v2-otp-ip:"+clientIPHash).Error; err != nil {
			return emailJob{}, 0, err
		}
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
	}
	challengeID, err := randomToken("efec_", 18)
	if err != nil {
		return emailJob{}, 0, err
	}
	otp, skipDelivery, err := s.generateChallengeOTP(normalizedEmail)
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
	return emailJob{challengeID: challengeID, to: normalizedEmail, otp: otp, skipDelivery: skipDelivery}, expiresAt, nil
}

func (s *Service) queueEmailChallenge(c *app.RequestContext, job emailJob, expiresAt int64) {
	if job.skipDelivery {
		reply(c, http.StatusAccepted, map[string]interface{}{
			"accepted": true, "challenge_id": job.challengeID, "expires_at": expiresAt,
		})
		return
	}
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

func (s *Service) createEmailBindingChallenge(ctx context.Context, c *app.RequestContext) {
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
	clientIPHash := s.clientIPHash(c)
	rateChecked, err := s.preflightEmailChallengeRate(ctx, normalizedEmail, clientIPHash, &sessionID)
	if errors.Is(err, errRateLimited) {
		fail(c, http.StatusTooManyRequests, "EMAIL_RATE_LIMITED", "too many verification attempts; try again later", nil)
		return
	}
	if err != nil {
		fail(c, http.StatusServiceUnavailable, "EMAIL_RATE_LIMIT_UNAVAILABLE", "email verification is temporarily unavailable", nil)
		return
	}
	var job emailJob
	var expiresAt int64
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?, 0))`, keyedHash(s.otpPepper, normalizedEmail)).Error; err != nil {
			return err
		}
		var createErr error
		job, expiresAt, createErr = s.insertEmailChallenge(tx, normalizedEmail, "bind", &agentIDValue, &sessionID, clientIPHash, now, rateChecked)
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

func (s *Service) createEmailLoginChallenge(ctx context.Context, c *app.RequestContext) {
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
	if _, isTestAccount := s.fixedTestOTP(normalizedEmail); s.emailQueue == nil && !isTestAccount {
		fail(c, http.StatusServiceUnavailable, "EMAIL_DELIVERY_UNAVAILABLE", "email verification is temporarily unavailable", nil)
		return
	}
	now := time.Now().UnixMilli()
	clientIPHash := s.clientIPHash(c)
	rateChecked, err := s.preflightEmailChallengeRate(ctx, normalizedEmail, clientIPHash, nil)
	if errors.Is(err, errRateLimited) {
		fail(c, http.StatusTooManyRequests, "EMAIL_RATE_LIMITED", "too many verification attempts; try again later", nil)
		return
	}
	if err != nil {
		fail(c, http.StatusServiceUnavailable, "EMAIL_RATE_LIMIT_UNAVAILABLE", "email verification is temporarily unavailable", nil)
		return
	}
	var job emailJob
	var expiresAt int64
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?, 0))`, keyedHash(s.otpPepper, normalizedEmail)).Error; err != nil {
			return err
		}
		var bindings []struct {
			AgentID           int64  `gorm:"column:agent_id"`
			VerificationState string `gorm:"column:verification_state"`
		}
		if err := tx.Raw(`SELECT agent_id, verification_state FROM agent_email_bindings
			WHERE normalized_email = ? AND status = 'active'
			  AND verification_state IN ('verified', 'legacy_unverified')
			ORDER BY binding_id LIMIT 2`, normalizedEmail).Scan(&bindings).Error; err != nil {
			return err
		}
		// Lazy creation is a bounded fallback while the resumable backfill is
		// still running. It only proceeds when the canonical legacy owner is
		// unambiguous; conflicts receive the same public 202 envelope.
		if len(bindings) == 0 {
			var legacyOwners []struct {
				AgentID int64 `gorm:"column:agent_id"`
			}
			if err := tx.Raw(`SELECT agent_id FROM agents
				WHERE email_kind = 'legacy_real' AND lower(btrim(email)) = ?
				ORDER BY agent_id LIMIT 2`, normalizedEmail).Scan(&legacyOwners).Error; err != nil {
				return err
			}
			if len(legacyOwners) == 1 {
				insert := tx.Exec(`INSERT INTO agent_email_bindings
					(agent_id, normalized_email, normalization_version, verification_state, status,
					 created_at, updated_at)
					VALUES (?, ?, 1, 'legacy_unverified', 'active', ?, ?)
					ON CONFLICT DO NOTHING`, legacyOwners[0].AgentID, normalizedEmail, now, now)
				if insert.Error != nil {
					return insert.Error
				}
				if insert.RowsAffected == 1 {
					bindings = append(bindings, struct {
						AgentID           int64  `gorm:"column:agent_id"`
						VerificationState string `gorm:"column:verification_state"`
					}{AgentID: legacyOwners[0].AgentID, VerificationState: "legacy_unverified"})
				}
			}
		}
		var subject *int64
		if len(bindings) == 1 && bindings[0].AgentID != 0 {
			subject = &bindings[0].AgentID
		}
		var createErr error
		job, expiresAt, createErr = s.insertEmailChallenge(tx, normalizedEmail, req.Purpose, subject, nil, clientIPHash, now, rateChecked)
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
	principalValue, hasPrincipal := c.Get("principal_id")
	principalID, principalOK := principalValue.(int64)
	sessionValue, hasSession := c.Get("console_session_id")
	sessionID, sessionOK := sessionValue.(string)
	if !ok || !hasPrincipal || !principalOK || !hasSession || !sessionOK {
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
	var recoveryDetails map[string]interface{}
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
		var legacyOwners []struct {
			AgentID int64 `gorm:"column:agent_id"`
		}
		if err := tx.Raw(`SELECT agent_id FROM agents
			WHERE lower(btrim(email)) = ? AND email_kind = 'legacy_real' ORDER BY agent_id LIMIT 2`, normalizedEmail).Scan(&legacyOwners).Error; err != nil {
			return err
		}
		otherOwners := map[int64]struct{}{}
		for _, owner := range bindingOwners {
			if owner.AgentID != agentIDValue {
				otherOwners[owner.AgentID] = struct{}{}
			}
		}
		for _, owner := range legacyOwners {
			if owner.AgentID != agentIDValue {
				otherOwners[owner.AgentID] = struct{}{}
			}
		}
		if len(otherOwners) > 1 {
			return errConflict
		}
		for targetAgentID := range otherOwners {
			details, recoveryErr := s.prepareAccountRecovery(tx, agentIDValue, targetAgentID,
				principalID, sessionID, req.ChallengeID, normalizedEmail, now)
			if recoveryErr != nil {
				return recoveryErr
			}
			recoveryDetails = details
			return nil
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
		if err := tx.Exec(`UPDATE agents SET email = ?,
			email_kind = CASE WHEN email_kind = 'legacy_real' THEN 'legacy_real' ELSE 'v2_bound' END,
			email_verified_at = ?, updated_at = ?
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
	if recoveryDetails != nil {
		fail(c, http.StatusConflict, "EMAIL_UNAVAILABLE", "this email belongs to a recoverable historical Agent", recoveryDetails)
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
	return sqlState(err) == "23505"
}

func isForeignKeyViolation(err error) bool {
	return sqlState(err) == "23503"
}

// Both lib/pq and pgx expose SQLState(), while GORM's configured PostgreSQL
// driver currently returns pgx errors. Keeping the check driver-neutral makes
// the idempotency conflict path work in production as well as pq-based tests.
func sqlState(err error) string {
	var stateError interface{ SQLState() string }
	if errors.As(err, &stateError) {
		return stateError.SQLState()
	}
	var pqError *pq.Error
	if errors.As(err, &pqError) {
		return string(pqError.Code)
	}
	return ""
}

// ensureLegacyConsoleV2State is a lazy, idempotent safety net for accounts
// reached while the offline cursor backfill is still in progress. It performs
// three indexed inserts and never marks onboarding complete without canonical
// goal/intent/context data.
func ensureLegacyConsoleV2State(tx *gorm.DB, id, now int64) error {
	if err := tx.Exec(`INSERT INTO agent_context_heads (agent_id, current_revision, updated_at)
		VALUES (?, 0, ?) ON CONFLICT (agent_id) DO NOTHING`, id, now).Error; err != nil {
		return err
	}
	if err := tx.Exec(`INSERT INTO agent_onboarding_v2
		(agent_id, state, current_step, revision, created_at, updated_at)
		VALUES (?, 'migration_pending', 2, 1, ?, ?)
		ON CONFLICT (agent_id) DO NOTHING`, id, now, now).Error; err != nil {
		return err
	}
	if err := tx.Exec(`INSERT INTO agent_feed_v2_settings
		(agent_id, poll_interval_seconds, explicitly_set, updated_at)
		VALUES (?, 600, false, ?) ON CONFLICT (agent_id) DO NOTHING`, id, now).Error; err != nil {
		return err
	}
	var existingDraft struct {
		Revision  int64  `gorm:"column:revision"`
		DraftData string `gorm:"column:draft_data"`
		RequestID string `gorm:"column:request_id"`
	}
	if err := tx.Raw(`SELECT revision, draft_data::text AS draft_data, request_id
		FROM agent_onboarding_drafts WHERE agent_id = ? ORDER BY revision DESC LIMIT 1`, id).Scan(&existingDraft).Error; err != nil {
		return err
	}
	if existingDraft.Revision > 0 && existingDraft.RequestID != "legacy-lazy-v1" {
		return nil
	}
	var snapshot struct {
		AgentName        string `gorm:"column:agent_name"`
		Bio              string `gorm:"column:bio"`
		Country          string `gorm:"column:country"`
		ProfileData      string `gorm:"column:profile_data"`
		PublicCard       string `gorm:"column:public_card"`
		PrivateCard      string `gorm:"column:private_card"`
		RecurringPublish bool   `gorm:"column:recurring_publish"`
		AutoReplyPM      bool   `gorm:"column:auto_reply_pm"`
		AutoComment      bool   `gorm:"column:auto_comment"`
		ShowAddFriend    bool   `gorm:"column:show_add_friend"`
	}
	if err := tx.Raw(`SELECT a.agent_name, COALESCE(a.bio, '') AS bio,
		COALESCE(p.country, '') AS country, COALESCE(p.profile_data, '{}'::jsonb)::text AS profile_data,
		COALESCE(c.public_card, '{}'::jsonb)::text AS public_card,
		COALESCE(c.private_card, '{}'::jsonb)::text AS private_card,
		COALESCE(s.recurring_publish, true) AS recurring_publish,
		COALESCE(s.auto_reply_pm, true) AS auto_reply_pm,
		COALESCE(s.auto_comment, true) AS auto_comment,
		COALESCE(s.show_add_friend, true) AS show_add_friend
		FROM agents a
		LEFT JOIN agent_profiles p ON p.agent_id = a.agent_id
		LEFT JOIN agent_cards c ON c.agent_id = a.agent_id
		LEFT JOIN agent_settings s ON s.agent_id = a.agent_id
		WHERE a.agent_id = ?`, id).Scan(&snapshot).Error; err != nil {
		return err
	}
	profileData, err := decodeHistoricalCardObject(snapshot.ProfileData)
	if err != nil {
		return err
	}
	publicCard, err := decodeHistoricalCardObject(snapshot.PublicCard)
	if err != nil {
		return err
	}
	privateCard, err := decodeHistoricalCardObject(snapshot.PrivateCard)
	if err != nil {
		return err
	}
	draft := map[string]interface{}{
		"identity_card": historicalIdentityCard(snapshot.AgentName, snapshot.Bio, snapshot.Country, profileData, publicCard, privateCard),
		"security_boundary": map[string]interface{}{
			"recurring_publish": snapshot.RecurringPublish,
			"auto_reply_pm":     snapshot.AutoReplyPM,
			"auto_comment":      snapshot.AutoComment,
			"show_add_friend":   snapshot.ShowAddFriend,
		},
		"network_goal":   "",
		"intent_actions": []interface{}{},
	}
	draftJSON, draftObject, err := normalizeHistoricalOnboardingDraftJSON(mustJSON(draft))
	if err != nil {
		return err
	}
	if existingDraft.Revision > 0 {
		currentDraft, decodeErr := decodeJSONObject(json.RawMessage(existingDraft.DraftData))
		if decodeErr != nil {
			return decodeErr
		}
		draftObject = mergeHistoricalRecoveryDraft(currentDraft, draftObject)
		draftJSON, draftObject, err = normalizeHistoricalOnboardingDraftJSON(mustJSON(draftObject))
		if err != nil {
			return err
		}
	}
	provenanceJSON, err := json.Marshal(deriveInitialProvenance(draftObject, provenanceSystem, nil, now))
	if err != nil {
		return err
	}
	if existingDraft.Revision > 0 {
		return tx.Exec(`UPDATE agent_onboarding_drafts
			SET draft_data = ?::jsonb, field_provenance = ?::jsonb, request_id = 'legacy-lazy-v2'
			WHERE agent_id = ? AND revision = ? AND request_id = 'legacy-lazy-v1'`,
			string(draftJSON), string(provenanceJSON), id, existingDraft.Revision).Error
	}
	return tx.Exec(`INSERT INTO agent_onboarding_drafts
		(agent_id, revision, draft_data, field_provenance, actor_type, request_id, created_at)
		VALUES (?, 1, ?::jsonb, ?::jsonb, 'system_derived', 'legacy-lazy-v2', ?)
		ON CONFLICT DO NOTHING`, id, string(draftJSON), string(provenanceJSON), now).Error
}

func mergeHistoricalRecoveryDraft(current, historical map[string]interface{}) map[string]interface{} {
	currentIdentity, _ := current["identity_card"].(map[string]interface{})
	if currentIdentity == nil {
		currentIdentity = map[string]interface{}{}
		current["identity_card"] = currentIdentity
	}
	historicalIdentity, _ := historical["identity_card"].(map[string]interface{})
	for field, historicalValue := range historicalIdentity {
		currentValue, exists := currentIdentity[field]
		if !meaningfulDraftValue(currentValue, exists) && meaningfulDraftValue(historicalValue, true) {
			currentIdentity[field] = historicalValue
		}
	}
	return current
}

func mustJSON(value interface{}) json.RawMessage {
	encoded, _ := json.Marshal(value)
	return encoded
}

func decodeHistoricalCardObject(raw string) (map[string]interface{}, error) {
	if strings.TrimSpace(raw) == "" {
		return map[string]interface{}{}, nil
	}
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, err
	}
	return result, nil
}

func historicalIdentityCard(agentName, bio, country string, profileData, publicCard, privateCard map[string]interface{}) map[string]interface{} {
	identity := map[string]interface{}{}
	if value := strings.TrimSpace(agentName); value != "" {
		identity["agent_name"] = value
	} else if value, ok := historicalString("agent_name", publicCard); ok {
		identity["agent_name"] = value
	}
	if value := strings.TrimSpace(bio); value != "" {
		identity["bio"] = value
		identity["agent_description"] = value
	} else if value, ok := historicalString("agent_description", publicCard); ok {
		identity["bio"] = value
		identity["agent_description"] = value
	}
	for _, field := range []string{"human_description"} {
		if value, ok := historicalString(field, profileData, publicCard); ok {
			identity[field] = value
		}
	}
	for _, field := range []string{"working_languages", "seeking", "offering"} {
		if value, ok := historicalList(field, profileData, publicCard); ok {
			identity[field] = value
		}
	}
	if value, ok := historicalString("geo", profileData, privateCard); ok {
		identity["geo"] = value
	} else if value := strings.TrimSpace(country); value != "" {
		identity["geo"] = value
	}
	if value, ok := historicalString("timezone", profileData, privateCard); ok {
		identity["timezone"] = value
	}
	for _, field := range []string{"current_focus", "demands", "agent_status", "human_status", "interests_negative"} {
		if value, ok := historicalList(field, profileData, privateCard); ok {
			identity[field] = value
		}
	}
	return identity
}

func historicalString(field string, sources ...map[string]interface{}) (string, bool) {
	for _, source := range sources {
		if value, ok := source[field].(string); ok {
			if value = strings.TrimSpace(value); value != "" {
				return value, true
			}
		}
	}
	return "", false
}

func historicalList(field string, sources ...map[string]interface{}) ([]string, bool) {
	for _, source := range sources {
		items := make([]string, 0)
		switch value := source[field].(type) {
		case []interface{}:
			for _, item := range value {
				if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
					items = append(items, strings.TrimSpace(text))
				}
			}
		case []string:
			for _, item := range value {
				if text := strings.TrimSpace(item); text != "" {
					items = append(items, text)
				}
			}
		case string:
			items = strings.FieldsFunc(value, func(r rune) bool {
				return r == '·' || r == ',' || r == '，' || r == ';' || r == '；' || r == '\n' || r == '\r'
			})
			for index := range items {
				items[index] = strings.TrimSpace(items[index])
			}
		}
		if len(items) > 0 {
			return items, true
		}
	}
	return nil, false
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
		if err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(?, 0))`, keyedHash(s.otpPepper, normalizedEmail)).Error; err != nil {
			return err
		}
		var binding struct {
			BindingID         int64  `gorm:"column:binding_id"`
			AgentID           int64  `gorm:"column:agent_id"`
			VerificationState string `gorm:"column:verification_state"`
		}
		if err := tx.Raw(`SELECT binding_id, agent_id, verification_state
			FROM agent_email_bindings
			WHERE normalized_email = ? AND status = 'active' FOR UPDATE`, normalizedEmail).Scan(&binding).Error; err != nil {
			return err
		}
		if binding.BindingID == 0 || binding.AgentID != recoveredAgentID ||
			(binding.VerificationState != "verified" && binding.VerificationState != "legacy_unverified") {
			return errUnauthorized
		}
		if binding.VerificationState == "legacy_unverified" {
			if err := tx.Exec(`UPDATE agent_email_bindings
				SET verification_state = 'verified', verified_at = ?, updated_at = ?
				WHERE binding_id = ? AND verification_state = 'legacy_unverified'`, now, now, binding.BindingID).Error; err != nil {
				return err
			}
			if err := tx.Exec(`UPDATE agents SET email_verified_at = COALESCE(email_verified_at, ?), updated_at = ?
				WHERE agent_id = ?`, now, now, recoveredAgentID).Error; err != nil {
				return err
			}
		}
		if err := ensureLegacyConsoleV2State(tx, recoveredAgentID, now); err != nil {
			return err
		}
		var principal struct {
			PrincipalID int64 `gorm:"column:principal_id"`
		}
		if err := tx.Raw(`SELECT principal_id FROM agent_principals
			WHERE agent_id = ? AND revoked_at IS NULL AND status IN ('limited','active')
			ORDER BY principal_id LIMIT 1`, recoveredAgentID).Scan(&principal).Error; err != nil {
			return err
		}
		if principal.PrincipalID == 0 {
			recoveryKey := make([]byte, ed25519.PublicKeySize)
			if _, err := rand.Read(recoveryKey); err != nil {
				return err
			}
			if err := tx.Raw(`INSERT INTO agent_principals
				(agent_id, key_type, key_fingerprint, public_key, status, created_at, last_seen_at)
				VALUES (?, 'email-recovery-v1', ?, ?, 'limited', ?, ?)
				RETURNING principal_id`, recoveredAgentID,
				fingerprintForKeyType("email-recovery-v1", recoveryKey), recoveryKey, now, now).
				Scan(&principal.PrincipalID).Error; err != nil {
				return err
			}
		}
		consume := tx.Exec(`UPDATE v2_email_challenges SET status = 'consumed', consumed_at = ?
			WHERE challenge_id = ? AND status = 'pending'`, now, req.ChallengeID)
		if consume.Error != nil || consume.RowsAffected != 1 {
			return errUnauthorized
		}
		return tx.Exec(`INSERT INTO console_v2_sessions
			(session_id, session_secret_hash, agent_id, principal_id, csrf_secret_hash,
				 status, scopes, issued_at, idle_expires_at, absolute_expires_at, last_seen_at,
				 auth_method, recent_auth_at)
			VALUES (?, ?, ?, ?, ?, 'active', ?, ?, ?, ?, ?, 'email_otp', ?)`, sessionID, hashString(sessionSecret),
			recoveredAgentID, principal.PrincipalID, hashString(csrfSecret),
			pq.Array([]string{"console:onboarding", "console:read", "console:write"}), now,
			now+int64(consoleIdleTTL/time.Millisecond), now+int64(consoleAbsoluteTTL/time.Millisecond), now, now).Error
	})
	if errors.Is(err, errUnauthorized) || (!validOTP && err == nil) {
		fail(c, http.StatusUnauthorized, "OTP_INVALID", "verification code is invalid or expired", nil)
		return
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, "EMAIL_LOGIN_FAILED", "could not establish Console V2 session", nil)
		return
	}
	s.setConsoleCookie(c, sessionID+"."+sessionSecret, int(consoleAbsoluteTTL/time.Second))
	s.setCSRFCookie(c, csrfSecret, int(consoleAbsoluteTTL/time.Second))
	reply(c, http.StatusOK, map[string]interface{}{
		"agent_id": fmt.Sprintf("%d", recoveredAgentID), "csrf_token": csrfSecret,
	})
}
