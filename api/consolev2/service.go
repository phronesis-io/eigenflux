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
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/protocol"
	"github.com/lib/pq"
	redis "github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"eigenflux_server/kitex_gen/eigenflux/feed/feedservice"
	"eigenflux_server/kitex_gen/eigenflux/notification/notificationservice"
	"eigenflux_server/pkg/agentcard"
	"eigenflux_server/pkg/config"
	mailservice "eigenflux_server/pkg/email"
)

const (
	consoleCookieName = "ef_console_v2"
	csrfCookieName    = "ef_console_v2_csrf"
	accessTTL         = 15 * time.Minute
	refreshTTL        = 30 * 24 * time.Hour
	handoffTTL        = 15 * time.Minute
	grantTTL          = 5 * time.Minute
	proofClockSkew    = 5 * time.Minute
	maxRequestBytes   = 256 << 10
	maxAgentStreams   = 3
	maxProcessStreams = 1000
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
	db                       *gorm.DB
	idgen                    IDGenerator
	bootstrapSecret          string
	otpPepper                string
	testEmailPatterns        []string
	testOTP                  string
	publicURL                string
	secureCookie             bool
	emailSender              mailservice.Sender
	emailQueue               chan emailJob
	feedClient               feedservice.Client
	notificationClient       notificationservice.Client
	enableFeed               bool
	enableControl            bool
	enableAttentionV1        bool
	enableCommunication      bool
	enablePublicRegistration bool
	registrationLimits       registrationRateLimits
	activityMu               sync.Mutex
	activityConnections      map[int64]int
	activityTotal            int
	activityWakeOnce         sync.Once
	activityWakeMu           sync.RWMutex
	activityWakeSubs         map[int64]map[chan struct{}]struct{}
	redisClient              *redis.Client
	communicationOnce        sync.Once
	communicationWakeMu      sync.RWMutex
	communicationSubs        map[int64]map[chan communicationWakeEvent]struct{}
	communicationMu          sync.Mutex
	communicationConnections map[int64]int
	communicationTotal       int
	controlWakeOnce          sync.Once
	controlWakeMu            sync.RWMutex
	controlWakeSubs          map[int64]map[chan int64]struct{}
	controlConnections       map[int64]int
	controlTotal             int
	processStreamMu          sync.Mutex
	processStreamTotal       int
	telemetryMu              sync.Mutex
	telemetryRates           map[string]telemetryRateState
	telemetryNextSweep       time.Time
	trustedProxyNets         []*net.IPNet
}

func (s *Service) tryAcquireProcessStream() bool {
	s.processStreamMu.Lock()
	defer s.processStreamMu.Unlock()
	if s.processStreamTotal >= maxProcessStreams {
		return false
	}
	s.processStreamTotal++
	return true
}

func (s *Service) releaseProcessStream() {
	s.processStreamMu.Lock()
	if s.processStreamTotal > 0 {
		s.processStreamTotal--
	}
	s.processStreamMu.Unlock()
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
	trustedProxyNets := make([]*net.IPNet, 0, len(cfg.ConsoleV2TrustedProxyCIDRs))
	for _, cidr := range cfg.ConsoleV2TrustedProxyCIDRs {
		_, network, parseErr := net.ParseCIDR(strings.TrimSpace(cidr))
		if parseErr != nil {
			return nil, errors.New("CONSOLE_V2_TRUSTED_PROXY_CIDRS contains an invalid CIDR")
		}
		trustedProxyNets = append(trustedProxyNets, network)
	}
	registrationLimits := registrationRateLimits{
		Window:    time.Duration(cfg.ConsoleV2Registration.WindowSec) * time.Second,
		IP:        int64(cfg.ConsoleV2Registration.IPLimit),
		Subnet:    int64(cfg.ConsoleV2Registration.SubnetLimit),
		PublicKey: int64(cfg.ConsoleV2Registration.KeyLimit),
		Global:    int64(cfg.ConsoleV2Registration.GlobalLimit),
	}
	if cfg.EnablePublicRegistration && !registrationLimits.valid() {
		return nil, errors.New("public Agent registration limits must all be positive")
	}
	if cfg.EnablePublicRegistration && strings.TrimSpace(cfg.ConsoleV2BootstrapSecret) == "" {
		return nil, errors.New("CONSOLE_V2_BOOTSTRAP_SECRET is required for public Agent registration")
	}
	if cfg.EnableAgentAttentionV1 && !cfg.EnableControlChannelV2 {
		return nil, errors.New("ENABLE_AGENT_ATTENTION_V1 requires ENABLE_CONTROL_CHANNEL_V2")
	}
	service := &Service{
		db:                       gdb,
		idgen:                    idgen,
		bootstrapSecret:          cfg.ConsoleV2BootstrapSecret,
		otpPepper:                cfg.ConsoleV2OTPPepper,
		testEmailPatterns:        append([]string(nil), cfg.OfficialTestEmailSuffixes...),
		testOTP:                  strings.TrimSpace(cfg.OfficialTestOTP),
		publicURL:                publicURL,
		secureCookie:             parsed.Scheme == "https",
		enableFeed:               cfg.EnableFeedV2,
		enableControl:            cfg.EnableControlChannelV2,
		enableAttentionV1:        cfg.EnableAgentAttentionV1,
		enableCommunication:      cfg.EnableCommunicationV2,
		enablePublicRegistration: cfg.EnablePublicRegistration,
		registrationLimits:       registrationLimits,
		activityConnections:      make(map[int64]int),
		activityWakeSubs:         make(map[int64]map[chan struct{}]struct{}),
		communicationSubs:        make(map[int64]map[chan communicationWakeEvent]struct{}),
		communicationConnections: make(map[int64]int),
		controlWakeSubs:          make(map[int64]map[chan int64]struct{}),
		controlConnections:       make(map[int64]int),
		telemetryRates:           make(map[string]telemetryRateState),
		trustedProxyNets:         trustedProxyNets,
	}
	if strings.TrimSpace(cfg.ResendApiKey) != "" {
		service.emailSender = mailservice.NewResendSender(cfg.ResendApiKey, cfg.ResendFromEmail)
		service.startEmailWorkers(2, 256)
	}
	return service, nil
}

func (s *Service) fixedTestOTP(normalizedEmail string) (string, bool) {
	if s.testOTP == "" || !config.EmailMatchesAnyPattern(normalizedEmail, s.testEmailPatterns) {
		return "", false
	}
	return s.testOTP, true
}

func (s *Service) SetFeedClient(client feedservice.Client) {
	s.feedClient = client
}

func (s *Service) SetNotificationClient(client notificationservice.Client) {
	s.notificationClient = client
}

// loadIdempotentResponse is used only after a transaction lost a race on a
// revision or unique constraint. The normal mutation path pays no extra query;
// a concurrent retry with the same request hash receives the committed result.
func (s *Service) loadIdempotentResponse(agentID int64, operation, key, requestHash string, destination interface{}) (found, hashConflict bool, err error) {
	return loadIdempotentResponseFrom(s.db, agentID, operation, key, requestHash, destination)
}

func loadIdempotentResponseFrom(db *gorm.DB, agentID int64, operation, key, requestHash string, destination interface{}) (found, hashConflict bool, err error) {
	var row struct {
		RequestHash string `gorm:"column:request_hash"`
		Response    string `gorm:"column:response_snapshot"`
	}
	if err = db.Raw(`SELECT request_hash, response_snapshot::text AS response_snapshot
		FROM agent_idempotency_requests
		WHERE agent_id = ? AND operation = ? AND idempotency_key = ?`, agentID, operation, key).Scan(&row).Error; err != nil {
		return false, false, err
	}
	if row.RequestHash == "" {
		return false, false, nil
	}
	if row.RequestHash != requestHash {
		return true, true, nil
	}
	if err = json.Unmarshal([]byte(row.Response), destination); err != nil {
		return true, false, err
	}
	return true, false, nil
}

// SetRedisClient enables one shared Pub/Sub subscriber per API process. SSE
// connections are fanned out in memory and never allocate one Redis connection
// per browser.
func (s *Service) SetRedisClient(client *redis.Client) {
	if client == nil {
		return
	}
	s.redisClient = client
	s.activityWakeOnce.Do(func() { go s.runActivityWakeSubscriber() })
	if s.enableCommunication {
		s.communicationOnce.Do(func() { go s.runCommunicationWakeSubscriber() })
	}
	if s.enableControl {
		s.controlWakeOnce.Do(func() {
			go s.runControlOutboxDispatcher()
			go s.runControlWakeSubscriber()
		})
	}
}

// ConsoleBFFHandlers adapts an existing business handler to the isolated V2
// browser session. The business handler still receives agent_id from trusted
// server context; no browser-supplied subject identifier is accepted.
func (s *Service) ConsoleBFFHandlers(mutation bool, handler app.HandlerFunc) []app.HandlerFunc {
	noStore := func(ctx context.Context, c *app.RequestContext) {
		c.Header("Cache-Control", "private, no-store")
		c.Header("Pragma", "no-cache")
		c.Next(ctx)
	}
	return []app.HandlerFunc{s.consoleAuth(mutation), s.requireCompleted, noStore, handler}
}

func (s *Service) CommunicationEnabled() bool { return s.enableCommunication }

func (s *Service) CommunicationConversationsHandler() app.HandlerFunc {
	return s.listCommunicationConversations
}

func (s *Service) CommunicationFriendsHandler() app.HandlerFunc {
	return s.listCommunicationFriends
}

func (s *Service) CommunicationFriendRequestsHandler() app.HandlerFunc {
	return s.listCommunicationFriendRequests
}

func validConsoleSameOrigin(origin, host, expectedURL string) bool {
	expected, err := url.Parse(expectedURL)
	if err != nil || expected.Scheme == "" || expected.Host == "" || !strings.EqualFold(host, expected.Host) {
		return false
	}
	provided, err := url.Parse(origin)
	if err != nil || provided.User != nil || provided.RawQuery != "" || provided.Fragment != "" ||
		provided.Path != "" || provided.Scheme == "" || provided.Host == "" {
		return false
	}
	return strings.EqualFold(provided.Scheme, expected.Scheme) && strings.EqualFold(provided.Host, expected.Host)
}

func (s *Service) requireSameOrigin() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		if !validConsoleSameOrigin(string(c.GetHeader("Origin")), string(c.Host()), s.publicURL) {
			fail(c, http.StatusForbidden, "ORIGIN_INVALID", "Console V2 request origin is invalid", nil)
			c.Abort()
			return
		}
		c.Next(ctx)
	}
}

// Register exposes only V2 routes. The caller controls registration with the
// Console V2 feature flag, so disabled deployments retain the exact V1 surface.
func (s *Service) Register(h *server.Hertz) {
	h.POST("/api/v2/bootstrap-grants", s.issueBootstrapGrant)
	if s.enablePublicRegistration {
		h.POST("/api/v2/agent-identities/registration-challenges", s.issuePublicRegistrationChallenge)
	}
	h.POST("/api/v2/agent-identities/provision", s.provision)
	h.POST("/api/v2/agent-sessions/refresh-challenges", s.createRefreshChallenge)
	h.POST("/api/v2/agent-sessions/refresh", s.refreshAgentSession)
	h.POST("/api/v2/account-email-bindings/challenges", s.consoleAuth(true), s.createEmailBindingChallenge)
	h.POST("/api/v2/account-email-bindings/verify", s.consoleAuth(true), s.verifyEmailBinding)
	h.POST("/api/v2/auth/email/challenges", s.requireSameOrigin(), s.createEmailLoginChallenge)
	h.POST("/api/v2/auth/email/verify", s.requireSameOrigin(), s.verifyEmailLogin)
	h.POST("/api/v2/agents/me/principals/challenges", s.consoleAuth(true), s.createPrincipalChallenge)
	h.POST("/api/v2/agents/me/principals", s.consoleAuth(true), s.addPrincipal)
	h.GET("/api/v2/agents/me/principals", s.consoleAuth(false), s.listPrincipals)
	h.DELETE("/api/v2/agents/me/principals/:principal_id", s.consoleAuth(true), s.revokePrincipal)
	h.POST("/api/v2/console/handoffs", s.agentAuth("console:handoff:create"), s.createHandoff)
	h.POST("/api/v2/console/handoffs/exchange", s.requireSameOrigin(), s.exchangeHandoff)
	h.GET("/api/v2/console/session", s.consoleAuth(false), s.getConsoleSession)
	h.DELETE("/api/v2/console/session", s.consoleAuth(true), s.deleteConsoleSession)

	h.PUT("/api/v2/agents/me/onboarding-draft", s.agentAuth("onboarding:write"), s.putOnboardingDraft)
	h.PUT("/api/v2/console/onboarding-draft", s.consoleAuth(true), s.putOnboardingDraft)
	h.GET("/api/v2/agents/me/onboarding-draft", s.consoleAuth(false), s.getOnboardingDraft)
	h.POST("/api/v2/agents/me/onboarding-draft/confirm", s.consoleAuth(true), s.confirmOnboardingStep)
	h.GET("/api/v2/agents/me/control-context", s.consoleAuth(false), s.requireCompleted, s.getControlContext)
	h.GET("/api/v2/agent-context", s.agentAuth("context:read"), s.requireCompleted, s.getControlContext)
	h.PUT("/api/v2/agents/me/network-goal", s.consoleAuth(true), s.requireCompleted, s.putNetworkGoal)
	h.POST("/api/v2/agents/me/intent-actions", s.consoleAuth(true), s.requireCompleted, s.createIntentAction)
	h.PUT("/api/v2/agents/me/intent-actions/:intent_id", s.consoleAuth(true), s.requireCompleted, s.updateIntentAction)
	h.DELETE("/api/v2/agents/me/intent-actions/:intent_id", s.consoleAuth(true), s.requireCompleted, s.deleteIntentAction)
	h.PUT("/api/v2/agents/me/security-boundary", s.consoleAuth(true), s.requireCompleted, s.putSecurityBoundary)
	h.PUT("/api/v2/agents/me/profile/fields", s.consoleAuth(true), s.requireCompleted, s.putProfileFields)
	h.GET("/api/v2/console/activity", s.consoleAuth(false), s.requireCompleted, s.listActivity)
	h.GET("/api/v2/console/activity/stream", s.consoleAuth(false), s.requireCompleted, s.streamActivity)
	h.GET("/api/v2/console/today", s.consoleAuth(false), s.requireCompleted, s.getToday)
	h.POST("/api/v2/telemetry/events:batch", s.consoleAuth(true), s.recordTelemetryBatch)
	if s.enableFeed {
		h.POST("/api/v2/feed", s.agentAuth("feed:read"), s.pullFeedV2)
		h.GET("/api/v2/feed/items/:source_type/:source_id", s.agentAuth("feed:read"), s.getFeedSourceItem)
		h.GET("/api/v2/notifications/pending", s.agentAuth("feed:read"), s.listPendingNotifications)
		h.POST("/api/v2/notifications/ack", s.agentAuth("notifications:ack"), s.ackPendingNotifications)
	}
	if s.enableControl {
		h.POST("/api/v2/agent-commands", s.consoleAuth(true), s.requireCompleted, s.createAgentCommand)
		h.GET("/api/v2/agent-commands/pending", s.agentAuth("commands:claim"), s.listPendingCommands)
		h.POST("/api/v2/agent-commands/:command_id/claim", s.agentAuth("commands:claim"), s.claimAgentCommand)
		h.POST("/api/v2/agent-commands/:command_id/complete", s.agentAuth("commands:claim"), s.completeAgentCommand)
		h.POST("/api/v2/runtime/heartbeat", s.agentAuth("commands:claim"), s.runtimeHeartbeat)
		h.GET("/api/v2/runtime/control/stream", s.agentAuth("commands:claim"), s.streamRuntimeControl)
	}
	if s.enableAttentionV1 {
		h.POST("/api/v2/agent-attention-items:publish", s.agentAuth("attention:write"), s.requireCompleted, s.publishAttentionItems)
		h.GET("/api/v2/console/attention-items", s.consoleAuth(false), s.requireCompleted, s.listAttentionItems)
		h.GET("/api/v2/console/attention-items/:attention_id", s.consoleAuth(false), s.requireCompleted, s.getAttentionItem)
		h.GET("/api/v2/console/attention-items/:attention_id/source", s.consoleAuth(false), s.requireCompleted, s.getAttentionSource)
		h.POST("/api/v2/console/attention-items/:attention_id/respond", s.consoleAuth(true), s.requireCompleted, s.respondAttentionItem)
		h.POST("/api/v2/console/attention-items/:attention_id/dismiss", s.consoleAuth(true), s.requireCompleted, s.dismissAttentionItem)
	}
	if s.enableCommunication {
		h.GET("/api/v2/console/pm/conversations", s.consoleAuth(false), s.requireCompleted, s.listCommunicationConversations)
		h.GET("/api/v2/console/pm/conversations/:conv_id/messages", s.consoleAuth(false), s.requireCompleted, s.listCommunicationMessages)
		h.GET("/api/v2/console/relations/friend-requests", s.consoleAuth(false), s.requireCompleted, s.listCommunicationFriendRequests)
		h.GET("/api/v2/console/relations/friends", s.consoleAuth(false), s.requireCompleted, s.listCommunicationFriends)
		h.GET("/api/v2/console/events/ws", s.consoleAuth(false), s.requireCompleted, s.streamCommunicationEvents)
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
	return fingerprintForKeyType("ed25519-v1", publicKey)
}

func fingerprintForKeyType(keyType string, publicKey []byte) string {
	h := sha256.New()
	_, _ = h.Write([]byte(keyType + "\x00"))
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
	SessionID   int64          `gorm:"column:session_id"`
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
		err := s.db.Raw(`SELECT cs.session_id, p.agent_id, p.principal_id, p.status, cs.scopes
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
		c.Set("agent_credential_session_id", principal.SessionID)
		go agentcard.TouchLastActive(context.Background(), s.redisClient, principal.AgentID)
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
	AuthMethod     string         `gorm:"column:auth_method"`
	RecentAuthAt   *int64         `gorm:"column:recent_auth_at"`
}

func (s *Service) consoleAuth(requireCSRF bool) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		if requireCSRF && !validConsoleSameOrigin(string(c.GetHeader("Origin")), string(c.Host()), s.publicURL) {
			fail(c, http.StatusForbidden, "ORIGIN_INVALID", "Console V2 request origin is invalid", nil)
			c.Abort()
			return
		}
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
			s.idle_expires_at, s.absolute_expires_at, s.last_seen_at,
			s.auth_method, s.recent_auth_at
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
		c.Set("console_auth_method", session.AuthMethod)
		if session.RecentAuthAt != nil {
			c.Set("console_recent_auth_at", *session.RecentAuthAt)
		}
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
