// Package consolev2 implements the isolated Console V2 authentication and
// onboarding control plane. It intentionally does not share V1 bearer tokens,
// cookies, DTOs, or route handlers.
package consolev2

import (
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/protocol"
	"github.com/lib/pq"
	"gorm.io/gorm"

	"eigenflux_server/kitex_gen/eigenflux/feed/feedservice"
	"eigenflux_server/pkg/config"
	mailservice "eigenflux_server/pkg/email"
)

const (
	consoleCookieName = "ef_console_v2"
	csrfCookieName    = "ef_console_v2_csrf"
	accessTTL         = 15 * time.Minute
	refreshTTL        = 30 * 24 * time.Hour
	handoffTTL        = 5 * time.Minute
	grantTTL          = 5 * time.Minute
	proofClockSkew    = 5 * time.Minute
	maxRequestBytes   = 256 << 10
)

var (
	errConflict           = errors.New("conflict")
	errUnauthorized       = errors.New("unauthorized")
	errOnboardingRequired = errors.New("onboarding required")
)

type IDGenerator interface {
	NextID() (int64, error)
}

type Service struct {
	db                  *gorm.DB
	idgen               IDGenerator
	bootstrapSecret     string
	otpPepper           string
	publicURL           string
	secureCookie        bool
	emailSender         mailservice.Sender
	emailQueue          chan emailJob
	feedClient          feedservice.Client
	enableFeed          bool
	enableControl       bool
	enableCommunication bool
	activityMu          sync.Mutex
	activityConnections map[int64]int
	telemetryMu         sync.Mutex
	telemetryRates      map[string]telemetryRateState
}

func NewService(gdb *gorm.DB, idgen IDGenerator, cfg *config.Config) (*Service, error) {
	if gdb == nil || idgen == nil || cfg == nil {
		return nil, errors.New("console v2 requires database, id generator, and config")
	}
	publicURL := strings.TrimRight(strings.TrimSpace(cfg.ConsoleV2PublicURL), "/")
	parsed, err := url.Parse(publicURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, errors.New("CONSOLE_V2_PUBLIC_URL must be an absolute URL")
	}
	if strings.TrimSpace(cfg.ConsoleV2OTPPepper) == "" {
		return nil, errors.New("CONSOLE_V2_OTP_PEPPER is required")
	}
	service := &Service{
		db:                  gdb,
		idgen:               idgen,
		bootstrapSecret:     cfg.ConsoleV2BootstrapSecret,
		otpPepper:           cfg.ConsoleV2OTPPepper,
		publicURL:           publicURL,
		secureCookie:        parsed.Scheme == "https",
		enableFeed:          cfg.EnableFeedV2,
		enableControl:       cfg.EnableControlChannelV2,
		enableCommunication: cfg.EnableCommunicationV2,
		activityConnections: make(map[int64]int),
		telemetryRates:      make(map[string]telemetryRateState),
	}
	if strings.TrimSpace(cfg.ResendApiKey) != "" {
		service.emailSender = mailservice.NewResendSender(cfg.ResendApiKey, cfg.ResendFromEmail)
		service.startEmailWorkers(2, 256)
	}
	return service, nil
}

func (s *Service) SetFeedClient(client feedservice.Client) {
	s.feedClient = client
}

// ConsoleBFFHandlers adapts an existing business handler to the isolated V2
// browser session. The business handler still receives agent_id from trusted
// server context; no browser-supplied subject identifier is accepted.
func (s *Service) ConsoleBFFHandlers(mutation bool, handler app.HandlerFunc) []app.HandlerFunc {
	return []app.HandlerFunc{s.consoleAuth(mutation), s.requireCompleted, handler}
}

// Register exposes only V2 routes. The caller controls registration with the
// Console V2 feature flag, so disabled deployments retain the exact V1 surface.
func (s *Service) Register(h *server.Hertz) {
	h.POST("/api/v2/bootstrap-grants", s.issueBootstrapGrant)
	h.POST("/api/v2/agent-identities/provision", s.provision)
	h.POST("/api/v2/agent-sessions/refresh-challenges", s.createRefreshChallenge)
	h.POST("/api/v2/agent-sessions/refresh", s.refreshAgentSession)
	h.POST("/api/v2/account-email-bindings/challenges", s.consoleAuth(true), s.createEmailBindingChallenge)
	h.POST("/api/v2/account-email-bindings/verify", s.consoleAuth(true), s.verifyEmailBinding)
	h.POST("/api/v2/auth/email/challenges", s.createEmailLoginChallenge)
	h.POST("/api/v2/auth/email/verify", s.verifyEmailLogin)
	h.POST("/api/v2/console/handoffs", s.agentAuth("console:handoff:create"), s.createHandoff)
	h.POST("/api/v2/console/handoffs/exchange", s.exchangeHandoff)
	h.GET("/api/v2/console/session", s.consoleAuth(false), s.getConsoleSession)
	h.DELETE("/api/v2/console/session", s.consoleAuth(true), s.deleteConsoleSession)

	h.PUT("/api/v2/agents/me/onboarding-draft", s.agentAuth("onboarding:write"), s.putOnboardingDraft)
	h.PUT("/api/v2/console/onboarding-draft", s.consoleAuth(true), s.putOnboardingDraft)
	h.GET("/api/v2/agents/me/onboarding-draft", s.consoleAuth(false), s.getOnboardingDraft)
	h.POST("/api/v2/agents/me/onboarding-draft/confirm", s.consoleAuth(true), s.confirmOnboardingStep)
	h.GET("/api/v2/agents/me/control-context", s.consoleAuth(false), s.requireCompleted, s.getControlContext)
	h.PUT("/api/v2/agents/me/network-goal", s.consoleAuth(true), s.requireCompleted, s.putNetworkGoal)
	h.POST("/api/v2/agents/me/intent-actions", s.consoleAuth(true), s.requireCompleted, s.createIntentAction)
	h.PUT("/api/v2/agents/me/intent-actions/:intent_id", s.consoleAuth(true), s.requireCompleted, s.updateIntentAction)
	h.DELETE("/api/v2/agents/me/intent-actions/:intent_id", s.consoleAuth(true), s.requireCompleted, s.deleteIntentAction)
	h.PUT("/api/v2/agents/me/security-boundary", s.consoleAuth(true), s.requireCompleted, s.putSecurityBoundary)
	h.PUT("/api/v2/agents/me/profile/fields", s.consoleAuth(true), s.requireCompleted, s.putProfileFields)
	h.GET("/api/v2/console/activity", s.consoleAuth(false), s.requireCompleted, s.listActivity)
	h.GET("/api/v2/console/activity/stream", s.consoleAuth(false), s.requireCompleted, s.streamActivity)
	h.POST("/api/v2/telemetry/events:batch", s.consoleAuth(true), s.recordTelemetryBatch)
	if s.enableFeed {
		h.POST("/api/v2/feed/batches", s.agentAuth("feed:read"), s.createFeedBatch)
		h.POST("/api/v2/feed/batches/:batch_id/lease:renew", s.agentAuth("feed:read"), s.renewFeedLease)
		h.POST("/api/v2/feed/batches/:batch_id/ack", s.agentAuth("feed:ack"), s.ackFeedBatch)
	}
	if s.enableControl {
		h.POST("/api/v2/agent-commands", s.consoleAuth(true), s.requireCompleted, s.createAgentCommand)
		h.GET("/api/v2/agent-commands/pending", s.agentAuth("commands:claim"), s.listPendingCommands)
		h.POST("/api/v2/agent-commands/:command_id/claim", s.agentAuth("commands:claim"), s.claimAgentCommand)
		h.POST("/api/v2/agent-commands/:command_id/complete", s.agentAuth("commands:claim"), s.completeAgentCommand)
	}
	if s.enableCommunication {
		h.GET("/api/v2/console/pm/conversations", s.consoleAuth(false), s.requireCompleted, s.listCommunicationConversations)
		h.GET("/api/v2/console/pm/conversations/:conv_id/messages", s.consoleAuth(false), s.requireCompleted, s.listCommunicationMessages)
		h.GET("/api/v2/console/relations/friend-requests", s.consoleAuth(false), s.requireCompleted, s.listCommunicationFriendRequests)
		h.GET("/api/v2/console/relations/friends", s.consoleAuth(false), s.requireCompleted, s.listCommunicationFriends)
	}
}

type apiError struct {
	Code    string      `json:"code"`
	Message string      `json:"message"`
	Details interface{} `json:"details,omitempty"`
}

func reply(c *app.RequestContext, status int, data interface{}) {
	c.Header("Cache-Control", "private, no-store")
	c.JSON(status, map[string]interface{}{"data": data})
}

func fail(c *app.RequestContext, status int, code, message string, details interface{}) {
	c.Header("Cache-Control", "private, no-store")
	c.JSON(status, map[string]interface{}{"error": apiError{Code: code, Message: message, Details: details}})
}

func decodeBody(c *app.RequestContext, dst interface{}) error {
	raw, err := c.Body()
	if err != nil {
		return err
	}
	if len(raw) == 0 || len(raw) > maxRequestBytes {
		return errors.New("request body is empty or too large")
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

func randomToken(prefix string, size int) (string, error) {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b), nil
}

func hashString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func keyedHash(secret, value string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil))
}

func decodePublicKey(encoded string) (ed25519.PublicKey, error) {
	b, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		b, err = base64.StdEncoding.DecodeString(encoded)
	}
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("public_key must be a canonical 32-byte Ed25519 key")
	}
	return ed25519.PublicKey(b), nil
}

func fingerprint(publicKey ed25519.PublicKey) string {
	h := sha256.New()
	_, _ = h.Write([]byte("ed25519-v1\x00"))
	_, _ = h.Write(publicKey)
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

func containsScope(scopes pq.StringArray, wanted string) bool {
	for _, scope := range scopes {
		if scope == wanted {
			return true
		}
	}
	return false
}

func (s *Service) setConsoleCookie(c *app.RequestContext, value string, maxAge int) {
	c.SetCookie(consoleCookieName, value, maxAge, "/", "", protocol.CookieSameSiteLaxMode, s.secureCookie, true)
	c.Header("Referrer-Policy", "no-referrer")
}

func (s *Service) setCSRFCookie(c *app.RequestContext, value string, maxAge int) {
	c.SetCookie(csrfCookieName, value, maxAge, "/", "", protocol.CookieSameSiteStrictMode, s.secureCookie, false)
}

type agentPrincipal struct {
	AgentID     int64          `gorm:"column:agent_id"`
	PrincipalID int64          `gorm:"column:principal_id"`
	Status      string         `gorm:"column:status"`
	Scopes      pq.StringArray `gorm:"column:scopes;type:text[]"`
}

func (s *Service) agentAuth(requiredScope string) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		header := string(c.GetHeader("Authorization"))
		if !strings.HasPrefix(header, "Bearer efv2a_") {
			fail(c, http.StatusUnauthorized, "AGENT_AUTH_REQUIRED", "missing or invalid Agent V2 bearer token", nil)
			c.Abort()
			return
		}
		token := strings.TrimPrefix(header, "Bearer ")
		var principal agentPrincipal
		now := time.Now().UnixMilli()
		err := s.db.Raw(`SELECT p.agent_id, p.principal_id, p.status, cs.scopes
			FROM agent_credential_sessions cs
			JOIN agent_principals p ON p.principal_id = cs.principal_id
			WHERE cs.access_token_hash = ? AND cs.audience = 'agent_v2'
			  AND cs.revoked_at IS NULL AND cs.expires_at > ?
			  AND p.revoked_at IS NULL AND p.status IN ('limited','active')`, hashString(token), now).
			Scan(&principal).Error
		if err != nil || principal.AgentID == 0 || !containsScope(principal.Scopes, requiredScope) {
			fail(c, http.StatusUnauthorized, "AGENT_AUTH_INVALID", "Agent V2 token is expired or lacks the required scope", nil)
			c.Abort()
			return
		}
		c.Set("agent_id", principal.AgentID)
		c.Set("principal_id", principal.PrincipalID)
		c.Next(ctx)
	}
}

type consoleSession struct {
	SessionID      string         `gorm:"column:session_id"`
	AgentID        int64          `gorm:"column:agent_id"`
	PrincipalID    int64          `gorm:"column:principal_id"`
	SecretHash     string         `gorm:"column:session_secret_hash"`
	CSRFSecretHash string         `gorm:"column:csrf_secret_hash"`
	Scopes         pq.StringArray `gorm:"column:scopes;type:text[]"`
	IdleExpiresAt  int64          `gorm:"column:idle_expires_at"`
	AbsoluteExpiry int64          `gorm:"column:absolute_expires_at"`
	LastSeenAt     int64          `gorm:"column:last_seen_at"`
}

func (s *Service) consoleAuth(requireCSRF bool) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		parts := strings.SplitN(string(c.Cookie(consoleCookieName)), ".", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			fail(c, http.StatusUnauthorized, "CONSOLE_SESSION_REQUIRED", "Console V2 session is required", nil)
			c.Abort()
			return
		}
		var session consoleSession
		now := time.Now().UnixMilli()
		err := s.db.Raw(`SELECT s.session_id, s.agent_id, s.principal_id,
				s.session_secret_hash, s.csrf_secret_hash, s.scopes,
				s.idle_expires_at, s.absolute_expires_at, s.last_seen_at
			FROM console_v2_sessions s
			JOIN agent_principals p ON p.principal_id = s.principal_id
			WHERE s.session_id = ? AND s.status = 'active'
			  AND s.idle_expires_at > ? AND s.absolute_expires_at > ?
			  AND p.revoked_at IS NULL AND p.status IN ('limited','active')`, parts[0], now, now).
			Scan(&session).Error
		if err != nil || session.SessionID == "" || subtle.ConstantTimeCompare([]byte(session.SecretHash), []byte(hashString(parts[1]))) != 1 {
			fail(c, http.StatusUnauthorized, "CONSOLE_SESSION_INVALID", "Console V2 session is invalid or expired", nil)
			c.Abort()
			return
		}
		if requireCSRF {
			csrf := string(c.GetHeader("X-CSRF-Token"))
			if csrf == "" || subtle.ConstantTimeCompare([]byte(session.CSRFSecretHash), []byte(hashString(csrf))) != 1 {
				fail(c, http.StatusForbidden, "CSRF_INVALID", "valid X-CSRF-Token is required", nil)
				c.Abort()
				return
			}
		}
		// Sliding activity is throttled to one write per five minutes.
		if now-session.LastSeenAt >= int64(5*time.Minute/time.Millisecond) {
			idle := now + int64(30*time.Minute/time.Millisecond)
			if idle > session.AbsoluteExpiry {
				idle = session.AbsoluteExpiry
			}
			s.db.Exec(`UPDATE console_v2_sessions SET last_seen_at = ?, idle_expires_at = ?
				WHERE session_id = ? AND last_seen_at = ?`, now, idle, session.SessionID, session.LastSeenAt)
		}
		c.Set("agent_id", session.AgentID)
		c.Set("principal_id", session.PrincipalID)
		c.Set("console_session_id", session.SessionID)
		c.Next(ctx)
	}
}

func agentID(c *app.RequestContext) (int64, bool) {
	v, ok := c.Get("agent_id")
	if !ok {
		return 0, false
	}
	id, ok := v.(int64)
	return id, ok && id > 0
}

func (s *Service) requireCompleted(ctx context.Context, c *app.RequestContext) {
	id, ok := agentID(c)
	if !ok {
		fail(c, http.StatusUnauthorized, "CONSOLE_SESSION_REQUIRED", "Console V2 session is required", nil)
		c.Abort()
		return
	}
	var state struct {
		State       string `gorm:"column:state"`
		CurrentStep int16  `gorm:"column:current_step"`
	}
	if err := s.db.Raw(`SELECT state, current_step FROM agent_onboarding_v2 WHERE agent_id = ?`, id).Scan(&state).Error; err != nil || state.State != "completed" {
		fail(c, http.StatusConflict, "ONBOARDING_REQUIRED", "complete onboarding before using this Console V2 capability", map[string]interface{}{"next_step": state.CurrentStep})
		c.Abort()
		return
	}
	c.Next(ctx)
}
