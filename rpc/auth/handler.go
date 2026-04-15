package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"eigenflux_server/kitex_gen/eigenflux/auth"
	"eigenflux_server/kitex_gen/eigenflux/base"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/email"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/rpc/auth/dal"
)

var emailRegexp = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)

const sessionDurationMs = int64(30 * 24 * time.Hour / time.Millisecond)

// AuthServiceImpl implements the kitex-generated AuthService interface.
type AuthServiceImpl struct {
	emailSender              email.Sender
	emailVerificationEnabled bool
	mockUniversalOTP         string
	mockOTPEmailSuffix       []string // e.g. ["@test.com"]
	mockOTPIPWhitelist       []string // e.g. ["10.0.0.1"]
	agentIDGen               interface {
		NextID() (int64, error)
	}
}

// isMockOTPEmail returns true if the email suffix matches the mock whitelist.
func (s *AuthServiceImpl) isMockOTPEmail(emailAddr string) bool {
	if len(s.mockOTPEmailSuffix) == 0 || s.mockUniversalOTP == "" {
		return false
	}
	lowerEmail := strings.ToLower(emailAddr)
	for _, suffix := range s.mockOTPEmailSuffix {
		if strings.HasSuffix(lowerEmail, suffix) {
			return true
		}
	}
	return false
}

// isMockOTPIPAllowed returns true if the client IP is in the mock whitelist.
func (s *AuthServiceImpl) isMockOTPIPAllowed(clientIP string) bool {
	if len(s.mockOTPIPWhitelist) == 0 {
		return false
	}
	for _, ip := range s.mockOTPIPWhitelist {
		if clientIP == ip {
			return true
		}
	}
	return false
}

func (s *AuthServiceImpl) isMockOTPBypass(emailAddr, clientIP string) bool {
	return s.isMockOTPEmail(emailAddr) && s.isMockOTPIPAllowed(clientIP)
}

func (s *AuthServiceImpl) isOTPMatched(code string, challenge *dal.AuthEmailChallenge) bool {
	challengeEmail := ""
	if challenge.Email != nil {
		challengeEmail = *challenge.Email
	}
	challengeIP := ""
	if challenge.ClientIP != nil {
		challengeIP = *challenge.ClientIP
	}
	if s.isMockOTPEmail(challengeEmail) {
		if !s.isMockOTPIPAllowed(challengeIP) {
			logger.Default().Warn("mock OTP email suffix matched but client IP not in whitelist", "emailMasked", logger.MaskEmail(challengeEmail), "clientIP", challengeIP)
			return false
		}
		return code == s.mockUniversalOTP
	}
	return sha256Hex(code) == challenge.CodeHash
}

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func generateOTP() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

func normalizeEmail(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

func checkIPRateLimit(ctx context.Context, key string, limit int64, window time.Duration, msg string) *base.BaseResp {
	count, _ := mq.RDB.Incr(ctx, key).Result()
	if count == 1 {
		mq.RDB.Expire(ctx, key, window)
	}
	if count > limit {
		return &base.BaseResp{Code: 429, Msg: msg}
	}
	return nil
}

func boolPtr(v bool) *bool {
	return &v
}

func (s *AuthServiceImpl) buildStartLoginDirectResp(loginResp *auth.VerifyLoginResp) *auth.StartLoginResp {
	resp := &auth.StartLoginResp{
		AgentId:                &loginResp.AgentId,
		AccessToken:            &loginResp.AccessToken,
		ExpiresAt:              &loginResp.ExpiresAt,
		IsNewAgent:             &loginResp.IsNewAgent,
		NeedsProfileCompletion: &loginResp.NeedsProfileCompletion,
		VerificationRequired:   boolPtr(false),
		BaseResp:               loginResp.BaseResp,
	}
	if loginResp.ProfileCompletedAt != nil {
		resp.ProfileCompletedAt = loginResp.ProfileCompletedAt
	}
	return resp
}

func (s *AuthServiceImpl) completeEmailLogin(ctx context.Context, normalizedEmail string, clientIP, userAgent *string) (*auth.VerifyLoginResp, error) {
	var agent *dal.Agent
	var err error
	isNew := false

	agent, err = dal.GetAgentByEmail(db.DB, normalizedEmail)
	if err != nil {
		return &auth.VerifyLoginResp{
			BaseResp: &base.BaseResp{Code: 500, Msg: "db error: " + err.Error()},
		}, nil
	}

	if agent == nil {
		if s.agentIDGen == nil {
			return &auth.VerifyLoginResp{
				BaseResp: &base.BaseResp{Code: 500, Msg: "agent id generator is not initialized"},
			}, nil
		}
		newAgentID, genErr := s.agentIDGen.NextID()
		if genErr != nil {
			return &auth.VerifyLoginResp{
				BaseResp: &base.BaseResp{Code: 500, Msg: "failed to generate agent id: " + genErr.Error()},
			}, nil
		}
		agent, err = dal.CreateMinimalAgent(db.DB, newAgentID, normalizedEmail)
		if err != nil {
			return &auth.VerifyLoginResp{
				BaseResp: &base.BaseResp{Code: 500, Msg: "failed to create agent: " + err.Error()},
			}, nil
		}
		isNew = true
	}

	now := time.Now().UnixMilli()
	_ = dal.SetEmailVerifiedAt(db.DB, agent.AgentID, now)

	accessToken := "at_" + uuid.New().String()
	tokenHash := sha256Hex(accessToken)
	expireAt := now + sessionDurationMs

	session := &dal.AgentSession{
		AgentID:   agent.AgentID,
		TokenHash: tokenHash,
		Status:    0,
		ExpireAt:  expireAt,
		ClientIP:  clientIP,
		UserAgent: userAgent,
	}
	if err := dal.CreateSession(db.DB, session); err != nil {
		return &auth.VerifyLoginResp{
			BaseResp: &base.BaseResp{Code: 500, Msg: "failed to create session: " + err.Error()},
		}, nil
	}

	cacheKey := "auth:session:" + tokenHash
	mq.RDB.Set(ctx, cacheKey, fmt.Sprintf("%d:%s", agent.AgentID, normalizedEmail), 10*time.Minute)

	latestAgent, _ := dal.GetAgentByEmail(db.DB, agent.Email)
	if latestAgent != nil {
		agent = latestAgent
	}

	needsProfile := agent.AgentName == "" || agent.Bio == ""
	if agent.ProfileCompletedAt != nil && agent.AgentName != "" && agent.Bio != "" {
		needsProfile = false
	}

	resp := &auth.VerifyLoginResp{
		AgentId:                agent.AgentID,
		AccessToken:            accessToken,
		ExpiresAt:              expireAt,
		IsNewAgent:             isNew,
		NeedsProfileCompletion: needsProfile,
		BaseResp:               &base.BaseResp{Code: 0, Msg: "success"},
	}
	if agent.ProfileCompletedAt != nil {
		resp.ProfileCompletedAt = agent.ProfileCompletedAt
	}
	return resp, nil
}

// StartLogin creates a challenge, sends OTP verification email, and returns challenge metadata.
func (s *AuthServiceImpl) StartLogin(ctx context.Context, req *auth.StartLoginReq) (*auth.StartLoginResp, error) {
	logger.Ctx(ctx).Info("StartLogin called", "method", req.LoginMethod, "emailMasked", logger.MaskEmail(req.Email))
	if req.LoginMethod != "email" {
		return &auth.StartLoginResp{
			BaseResp: &base.BaseResp{Code: 400, Msg: "unsupported login_method"},
		}, nil
	}

	normalizedEmail := normalizeEmail(req.Email)
	if !emailRegexp.MatchString(normalizedEmail) {
		return &auth.StartLoginResp{
			BaseResp: &base.BaseResp{Code: 400, Msg: "invalid email format"},
		}, nil
	}

	emailHash := sha256Hex(normalizedEmail)
	clientIP := ""
	if req.ClientIp != nil {
		clientIP = *req.ClientIp
	}
	mockBypass := s.isMockOTPBypass(normalizedEmail, clientIP)

	if !s.emailVerificationEnabled {
		loginResp, err := s.completeEmailLogin(ctx, normalizedEmail, req.ClientIp, req.UserAgent)
		if err != nil {
			return nil, err
		}
		return s.buildStartLoginDirectResp(loginResp), nil
	}

	// IP rate limit: 30 per 10 min. Each StartLogin call counts toward this
	// quota — even if the challenge/OTP is reused for the same email — so a
	// client cannot fan out email sends without bound.
	if clientIP != "" && !mockBypass {
		ipKey := "auth:login:start:email:ip:" + clientIP
		if resp := checkIPRateLimit(ctx, ipKey, 30, 10*time.Minute, "too many requests from this IP"); resp != nil {
			return &auth.StartLoginResp{BaseResp: resp}, nil
		}
	}

	// Within the 10-minute challenge validity window, repeated StartLogin for
	// the same email must return the same challenge_id and the same OTP. This
	// prevents agents from mixing up round-trips (verifying a stale code
	// against a freshly issued challenge).
	activeKey := "auth:login:email:active:" + emailHash
	var challengeID string
	var otpCode string
	var expireAt int64

	if val, gerr := mq.RDB.Get(ctx, activeKey).Result(); gerr == nil && val != "" {
		if sep := strings.IndexByte(val, ':'); sep > 0 {
			cachedID := val[:sep]
			cachedOTP := val[sep+1:]
			if existing, cerr := dal.GetChallenge(db.DB, cachedID); cerr == nil &&
				existing != nil && existing.Status == 0 &&
				existing.ExpireAt > time.Now().UnixMilli() {
				challengeID = cachedID
				otpCode = cachedOTP
				expireAt = existing.ExpireAt
			}
		}
	}

	if challengeID == "" {
		newID := "ch_" + uuid.New().String()
		newOTP, gerr := generateOTP()
		if gerr != nil {
			return &auth.StartLoginResp{
				BaseResp: &base.BaseResp{Code: 500, Msg: "failed to generate OTP"},
			}, nil
		}

		now := time.Now().UnixMilli()
		newExpireAt := now + 600_000 // 10 minutes in ms

		emailVal := normalizedEmail
		challenge := &dal.AuthEmailChallenge{
			ChallengeID:  newID,
			LoginMethod:  req.LoginMethod,
			Email:        &emailVal,
			CodeHash:     sha256Hex(newOTP),
			Status:       0,
			AttemptCount: 0,
			MaxAttempts:  5,
			ExpireAt:     newExpireAt,
			CreatedAt:    now,
			ClientIP:     req.ClientIp,
			UserAgent:    req.UserAgent,
		}

		if cerr := dal.CreateChallenge(db.DB, challenge); cerr != nil {
			return &auth.StartLoginResp{
				BaseResp: &base.BaseResp{Code: 500, Msg: "failed to create challenge: " + cerr.Error()},
			}, nil
		}

		mq.RDB.Set(ctx, activeKey, newID+":"+newOTP, 10*time.Minute)

		challengeID = newID
		otpCode = newOTP
		expireAt = newExpireAt
	}

	// Send email on every call (skip for mock OTP targets). Reuse of the
	// challenge does not suppress the email — the IP rate limit is the
	// throttle.
	if s.isMockOTPEmail(normalizedEmail) {
		if !mockBypass {
			logger.Ctx(ctx).Warn("mock OTP email suffix matched but client IP not in whitelist, rejecting", "emailMasked", logger.MaskEmail(normalizedEmail), "clientIP", clientIP)
			return &auth.StartLoginResp{
				BaseResp: &base.BaseResp{Code: 400, Msg: "invalid email format"},
			}, nil
		}
		logger.Ctx(ctx).Info("mock OTP target, skipping email send", "emailMasked", logger.MaskEmail(normalizedEmail), "clientIP", clientIP)
	} else {
		sendCtx := context.WithValue(ctx, email.ChallengeIDKey, challengeID)
		if err := s.emailSender.SendLoginVerifyMail(sendCtx, normalizedEmail, otpCode); err != nil {
			return &auth.StartLoginResp{
				BaseResp: &base.BaseResp{Code: 500, Msg: "failed to send email: " + err.Error()},
			}, nil
		}
	}

	expiresInSec := int32((expireAt - time.Now().UnixMilli()) / 1000)
	if expiresInSec < 0 {
		expiresInSec = 0
	}
	resendAfterSec := int32(0)

	return &auth.StartLoginResp{
		ChallengeId:          &challengeID,
		ExpiresInSec:         &expiresInSec,
		ResendAfterSec:       &resendAfterSec,
		VerificationRequired: boolPtr(true),
		BaseResp:             &base.BaseResp{Code: 0, Msg: "success"},
	}, nil
}

// VerifyLogin validates the OTP code and issues a session token.
func (s *AuthServiceImpl) VerifyLogin(ctx context.Context, req *auth.VerifyLoginReq) (*auth.VerifyLoginResp, error) {
	logger.Ctx(ctx).Info("VerifyLogin called", "challengeID", req.ChallengeId)
	if !s.emailVerificationEnabled {
		return &auth.VerifyLoginResp{
			BaseResp: &base.BaseResp{Code: 400, Msg: "email verification is disabled; call /api/v1/auth/login directly"},
		}, nil
	}

	if req.LoginMethod != "email" {
		return &auth.VerifyLoginResp{
			BaseResp: &base.BaseResp{Code: 400, Msg: "unsupported login_method"},
		}, nil
	}

	if req.Code == nil || *req.Code == "" {
		return &auth.VerifyLoginResp{
			BaseResp: &base.BaseResp{Code: 400, Msg: "code is required"},
		}, nil
	}

	challenge, err := dal.GetChallenge(db.DB, req.ChallengeId)
	if err != nil {
		if req.ClientIp != nil && *req.ClientIp != "" {
			ipKey := "auth:login:verify:email:ip:" + *req.ClientIp
			if resp := checkIPRateLimit(ctx, ipKey, 100, 10*time.Minute, "too many verify attempts from this IP"); resp != nil {
				return &auth.VerifyLoginResp{BaseResp: resp}, nil
			}
		}
		return &auth.VerifyLoginResp{
			BaseResp: &base.BaseResp{Code: 404, Msg: "challenge not found"},
		}, nil
	}

	clientIP := ""
	if req.ClientIp != nil {
		clientIP = *req.ClientIp
	}
	challengeEmail := ""
	if challenge.Email != nil {
		challengeEmail = *challenge.Email
	}

	// IP rate limit: 100 per 10 min, unless the request is using mock email+IP allowlist.
	if clientIP != "" && !s.isMockOTPBypass(challengeEmail, clientIP) {
		ipKey := "auth:login:verify:email:ip:" + clientIP
		if resp := checkIPRateLimit(ctx, ipKey, 100, 10*time.Minute, "too many verify attempts from this IP"); resp != nil {
			return &auth.VerifyLoginResp{BaseResp: resp}, nil
		}
	}

	now := time.Now().UnixMilli()

	if challenge.Status != 0 {
		return &auth.VerifyLoginResp{
			BaseResp: &base.BaseResp{Code: 400, Msg: "challenge is no longer valid"},
		}, nil
	}
	if challenge.ExpireAt < now {
		return &auth.VerifyLoginResp{
			BaseResp: &base.BaseResp{Code: 400, Msg: "challenge has expired"},
		}, nil
	}
	if challenge.AttemptCount >= challenge.MaxAttempts {
		return &auth.VerifyLoginResp{
			BaseResp: &base.BaseResp{Code: 400, Msg: "max attempts exceeded"},
		}, nil
	}

	// Verify OTP code.
	// In mock mode, allow universal OTP for local development/testing only.
	matched := s.isOTPMatched(*req.Code, challenge)

	if !matched {
		_ = dal.IncrementChallengeAttempts(db.DB, req.ChallengeId)
		// Re-fetch to check updated count
		updated, fetchErr := dal.GetChallenge(db.DB, req.ChallengeId)
		if fetchErr == nil && updated.AttemptCount >= updated.MaxAttempts {
			_ = dal.RevokeChallenge(db.DB, req.ChallengeId)
		}
		return &auth.VerifyLoginResp{
			BaseResp: &base.BaseResp{Code: 401, Msg: "invalid code"},
		}, nil
	}

	// Atomically consume challenge to prevent concurrent double-use.
	consumed, err := dal.ConsumeChallenge(db.DB, req.ChallengeId, now)
	if err != nil {
		return &auth.VerifyLoginResp{
			BaseResp: &base.BaseResp{Code: 500, Msg: "failed to consume challenge"},
		}, nil
	}
	if !consumed {
		return &auth.VerifyLoginResp{
			BaseResp: &base.BaseResp{Code: 400, Msg: "challenge is no longer valid"},
		}, nil
	}

	if challenge.Email == nil || *challenge.Email == "" {
		return &auth.VerifyLoginResp{
			BaseResp: &base.BaseResp{Code: 400, Msg: "no email associated with challenge"},
		}, nil
	}

	return s.completeEmailLogin(ctx, normalizeEmail(*challenge.Email), req.ClientIp, req.UserAgent)
}

// ValidateSession verifies an access token and returns the associated agent_id and email.
func (s *AuthServiceImpl) ValidateSession(ctx context.Context, req *auth.ValidateSessionReq) (*auth.ValidateSessionResp, error) {
	logger.Ctx(ctx).Debug("ValidateSession called")
	tokenHash := sha256Hex(req.AccessToken)

	// Check Redis cache
	cacheKey := "auth:session:" + tokenHash
	val, err := mq.RDB.Get(ctx, cacheKey).Result()
	if err == nil && val != "" {
		parts := strings.SplitN(val, ":", 2)
		var agentID int64
		var email string
		fmt.Sscanf(parts[0], "%d", &agentID)
		if len(parts) > 1 {
			email = parts[1]
		}
		if agentID > 0 {
			return &auth.ValidateSessionResp{
				AgentId:  agentID,
				Email:    &email,
				BaseResp: &base.BaseResp{Code: 0, Msg: "success"},
			}, nil
		}
	}

	// Cache miss: query DB
	session, err := dal.GetSessionByTokenHash(db.DB, tokenHash)
	if err != nil {
		return &auth.ValidateSessionResp{
			BaseResp: &base.BaseResp{Code: 401, Msg: "invalid or expired session"},
		}, nil
	}

	// Fetch email for agent
	var agentEmail string
	var agent dal.Agent
	if err := db.DB.Select("email").Where("agent_id = ?", session.AgentID).First(&agent).Error; err == nil {
		agentEmail = agent.Email
	}

	// Cache result, update last_seen_at and extend expire_at (sliding expiration)
	mq.RDB.Set(ctx, cacheKey, fmt.Sprintf("%d:%s", session.AgentID, agentEmail), 10*time.Minute)
	now := time.Now().UnixMilli()
	newExpireAt := now + sessionDurationMs
	if err := dal.UpdateSessionActivity(db.DB, session.SessionID, now, newExpireAt); err != nil {
		logger.Ctx(ctx).Error("failed to update session activity", "err", err, "sessionID", session.SessionID)
	}

	return &auth.ValidateSessionResp{
		AgentId:  session.AgentID,
		Email:    &agentEmail,
		BaseResp: &base.BaseResp{Code: 0, Msg: "success"},
	}, nil
}

// Logout revokes the session associated with the given access token.
func (s *AuthServiceImpl) Logout(ctx context.Context, req *auth.LogoutReq) (*auth.LogoutResp, error) {
	tokenHash := sha256Hex(req.AccessToken)

	if err := dal.RevokeSession(db.DB, tokenHash); err != nil {
		logger.Ctx(ctx).Error("logout: db revoke failed", "err", err)
	}

	if err := mq.RDB.Del(ctx, "auth:session:"+tokenHash).Err(); err != nil {
		logger.Ctx(ctx).Error("logout: redis del failed", "err", err)
	}

	return &auth.LogoutResp{
		BaseResp: &base.BaseResp{Code: 0, Msg: "logged out"},
	}, nil
}
