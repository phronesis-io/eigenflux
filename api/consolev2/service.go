// Package consolev2 implements the isolated Console V2 authentication and
// onboarding control plane. V2 keeps its bearer tokens and cookies isolated;
// authenticated aliases may reuse stable business handlers behind V2 auth.
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
	"github.com/lib/pq"
	redis "github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	agentcardapi "eigenflux_server/api/agentcard"
	apihandler "eigenflux_server/api/handler_gen/eigenflux/api"
	"eigenflux_server/api/middleware"
	"eigenflux_server/kitex_gen/eigenflux/feed/feedservice"
	"eigenflux_server/kitex_gen/eigenflux/notification/notificationservice"
	"eigenflux_server/pipeline/llm"
	"eigenflux_server/pkg/agentcard"
	"eigenflux_server/pkg/config"
	mailservice "eigenflux_server/pkg/email"
)

const (
	consoleCookieName           = "ef_console_v2"
	csrfCookieName              = "ef_console_v2_csrf"
	activeConsoleSlotCookieName = "ef_console_v2_active"
	maxConsoleAccountSlots      = 5
	// Browser sessions use a long idle window while retaining a hard upper
	// bound. Keep these values centralized so handoff and email-OTP login
	// cannot drift apart.
	consoleIdleTTL     = 30 * 24 * time.Hour
	consoleAbsoluteTTL = 180 * 24 * time.Hour
	accessTTL          = 15 * time.Minute
	refreshTTL         = 30 * 24 * time.Hour
	handoffTTL         = 15 * time.Minute
	grantTTL           = 5 * time.Minute
	proofClockSkew     = 5 * time.Minute
	maxRequestBytes    = 256 << 10
	maxAgentStreams    = 3
	maxProcessStreams  = 1000
)

var (
	errConflict              = errors.New("conflict")
	errUnauthorized          = errors.New("unauthorized")
	errOnboardingRequired    = errors.New("onboarding required")
	errCLIUpgradeRequired    = errors.New("CLI upgrade required")
	errConsoleAccountLimit   = errors.New("console account limit reached")
	errConsoleSessionInvalid = errors.New("console session invalid")
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
	todayBriefGenerator      todayBriefGenerator
	todayBriefSlots          chan struct{}
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
		todayBriefSlots:          make(chan struct{}, 4),
	}
	if strings.TrimSpace(cfg.LLMApiKey) != "" {
		service.todayBriefGenerator = &llmTodayBriefGenerator{
			client: llm.NewClient(cfg, nil).WithReasoningOff(),
		}
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
	h.POST("/api/v2/account-recoveries/:recovery_id/confirm", s.consoleAuth(true), s.confirmAccountRecovery)
	h.POST("/api/v2/auth/email/challenges", s.requireSameOrigin(), s.createEmailLoginChallenge)
	h.POST("/api/v2/auth/email/verify", s.requireSameOrigin(), s.verifyEmailLogin)
	h.POST("/api/v2/agents/me/principals/challenges", s.consoleAuth(true), s.createPrincipalChallenge)
	h.POST("/api/v2/agents/me/principals", s.consoleAuth(true), s.addPrincipal)
	h.GET("/api/v2/agents/me/principals", s.consoleAuth(false), s.listPrincipals)
	h.DELETE("/api/v2/agents/me/principals/:principal_id", s.consoleAuth(true), s.revokePrincipal)
	h.POST("/api/v2/console/handoffs", s.agentAuth("console:handoff:create"), s.createHandoff)
	h.PUT("/api/v2/agent-settings/heartbeat-compatibility", middleware.ClientInfoMiddleware(), s.agentAuth("commands:claim"), s.requireCompleted, s.reportHeartbeatCompatibility)
	h.POST("/api/v2/console/handoffs/exchange", s.requireSameOrigin(), s.exchangeHandoff)
	h.GET("/api/v2/console/session", s.consoleAuth(false), s.getConsoleSession)
	h.DELETE("/api/v2/console/session", s.consoleAuth(true), s.deleteConsoleSession)
	h.GET("/api/v2/console/accounts", s.consoleAuth(false), s.listConsoleAccounts)
	h.POST("/api/v2/console/accounts/:agent_id/activate", s.consoleAuth(true), s.activateConsoleAccount)
	h.DELETE("/api/v2/console/accounts/:agent_id", s.consoleAuth(true), s.removeConsoleAccount)

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
	h.GET("/api/v2/console/today/status", s.consoleAuth(false), s.requireCompleted, s.getTodayStatus)
	h.GET("/api/v2/console/today/brief", s.consoleAuth(false), s.requireCompleted, s.getTodayBrief)
	h.POST("/api/v2/telemetry/events:batch", s.consoleAuth(true), s.recordTelemetryBatch)
	if s.enableFeed {
		h.POST("/api/v2/feed", s.agentAuth("feed:read"), s.pullFeedV2)
		h.GET("/api/v2/feed/items/:source_type/:source_id", s.agentAuth("feed:read"), s.getFeedSourceItem)
		h.POST("/api/v2/feed/feedback", s.agentAuth("feed:feedback"), s.requireCompleted, apihandler.BatchFeedback)
		h.POST("/api/v2/feed/events:batch", s.agentAuth("feed:feedback"), s.requireCompleted, apihandler.PushFeedEvents)
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
		h.POST("/api/v2/agent-attention-items/prefill", s.agentAuth("attention:prefill"), s.requireIncomplete, s.prefillAttentionItems)
		h.POST("/api/v2/agent-attention-items:publish", s.agentAuth("attention:write"), s.requireCompleted, s.publishAttentionItems)
		h.GET("/api/v2/console/attention-items", s.consoleAuth(false), s.requireCompleted, s.listAttentionItems)
		h.GET("/api/v2/console/attention-items/:attention_id", s.consoleAuth(false), s.requireCompleted, s.getAttentionItem)
		h.GET("/api/v2/console/attention-items/:attention_id/source", s.consoleAuth(false), s.requireCompleted, s.getAttentionSource)
		h.POST("/api/v2/console/attention-items/:attention_id/respond", s.consoleAuth(true), s.requireCompleted, s.respondAttentionItem)
		h.POST("/api/v2/console/attention-items/:attention_id/dismiss", s.consoleAuth(true), s.requireCompleted, s.dismissAttentionItem)
	}
	h.POST("/api/v2/pm/messages", s.agentAuth("communication:write"), s.requireCompleted, apihandler.SendPM)
	h.GET("/api/v2/pm/messages", s.agentAuth("communication:read"), s.requireCompleted, apihandler.FetchPM)
	h.GET("/api/v2/pm/conversations", s.agentAuth("communication:read"), s.requireCompleted, apihandler.ListConversations)
	h.GET("/api/v2/pm/conversations/history", s.agentAuth("communication:read"), s.requireCompleted, apihandler.GetConvHistory)
	h.POST("/api/v2/pm/conversations/close", s.agentAuth("communication:write"), s.requireCompleted, apihandler.CloseConv)
	h.GET("/api/v2/relations/friend-requests", s.agentAuth("relations:read"), s.requireCompleted, apihandler.ListFriendRequests)
	h.POST("/api/v2/relations/friend-requests", s.agentAuth("relations:write"), s.requireCompleted, apihandler.SendFriendRequest)
	h.POST("/api/v2/relations/friend-requests/handle", s.agentAuth("relations:write"), s.requireCompleted, apihandler.HandleFriendRequest)
	h.GET("/api/v2/relations/friends", s.agentAuth("relations:read"), s.requireCompleted, apihandler.ListFriends)
	h.POST("/api/v2/relations/friends/unfriend", s.agentAuth("relations:write"), s.requireCompleted, apihandler.Unfriend)
	h.POST("/api/v2/relations/friends/block", s.agentAuth("relations:write"), s.requireCompleted, apihandler.BlockUser)
	h.POST("/api/v2/relations/friends/unblock", s.agentAuth("relations:write"), s.requireCompleted, apihandler.UnblockUser)
	h.POST("/api/v2/relations/friends/remark", s.agentAuth("relations:write"), s.requireCompleted, apihandler.UpdateFriendRemark)
	if s.enableCommunication {
		h.GET("/api/v2/console/pm/conversations", s.consoleAuth(false), s.requireCompleted, s.listCommunicationConversations)
		h.GET("/api/v2/console/pm/conversations/:conv_id/messages", s.consoleAuth(false), s.requireCompleted, s.listCommunicationMessages)
		h.GET("/api/v2/console/relations/friend-requests", s.consoleAuth(false), s.requireCompleted, s.listCommunicationFriendRequests)
		h.GET("/api/v2/console/relations/friends", s.consoleAuth(false), s.requireCompleted, s.listCommunicationFriends)
		h.GET("/api/v2/console/events/ws", s.consoleAuth(false), s.requireCompleted, s.streamCommunicationEvents)
	}

	h.POST("/api/v2/broadcasts", s.agentAuth("broadcast:write"), s.requireCompleted, apihandler.Publish)
	h.DELETE("/api/v2/broadcasts/:item_id", s.agentAuth("broadcast:write"), s.requireCompleted, apihandler.DeleteMyItem)
	h.GET("/api/v2/agents/me/broadcasts", s.agentAuth("profile:read"), s.requireCompleted, apihandler.GetMyItems)
	h.GET("/api/v2/agent-profile", s.agentAuth("profile:read"), s.requireCompleted, apihandler.GetMe)
	h.GET("/api/v2/agent-profile/card", s.agentAuth("profile:read"), s.requireCompleted, agentcardapi.GetMyCard)
	h.PUT("/api/v2/agent-profile/fields", s.agentAuth("profile:write"), s.requireCompleted, agentcardapi.PutProfileFields)
	h.GET("/api/v2/agent-settings", s.agentAuth("settings:read"), s.requireCompleted, apihandler.GetMySettings)
	h.PUT("/api/v2/agent-settings", middleware.ClientInfoMiddleware(), s.agentAuth("settings:write"), s.requireCompleted, apihandler.PutMySettings)

	// V2 aliases preserve the stable CLI request/response bodies while the CLI
	// transitions to the canonical routes above. They use Agent V2 auth only.
	h.GET("/api/v2/items/:item_id", s.agentAuth("feed:read"), s.requireCompleted, apihandler.GetItem)
	h.POST("/api/v2/items/feedback", s.agentAuth("feed:feedback"), s.requireCompleted, apihandler.BatchFeedback)
	h.POST("/api/v2/items/events", s.agentAuth("feed:feedback"), s.requireCompleted, apihandler.PushFeedEvents)
	h.POST("/api/v2/items/publish", s.agentAuth("broadcast:write"), s.requireCompleted, apihandler.Publish)
	h.GET("/api/v2/pm/fetch", s.agentAuth("communication:read"), s.requireCompleted, apihandler.FetchPM)
	h.POST("/api/v2/pm/send", s.agentAuth("communication:write"), s.requireCompleted, apihandler.SendPM)
	h.GET("/api/v2/pm/history", s.agentAuth("communication:read"), s.requireCompleted, apihandler.GetConvHistory)
	h.POST("/api/v2/pm/close", s.agentAuth("communication:write"), s.requireCompleted, apihandler.CloseConv)
	h.GET("/api/v2/relations/applications", s.agentAuth("relations:read"), s.requireCompleted, apihandler.ListFriendRequests)
	h.POST("/api/v2/relations/apply", s.agentAuth("relations:write"), s.requireCompleted, apihandler.SendFriendRequest)
	h.POST("/api/v2/relations/handle", s.agentAuth("relations:write"), s.requireCompleted, apihandler.HandleFriendRequest)
	h.POST("/api/v2/relations/unfriend", s.agentAuth("relations:write"), s.requireCompleted, apihandler.Unfriend)
	h.POST("/api/v2/relations/block", s.agentAuth("relations:write"), s.requireCompleted, apihandler.BlockUser)
	h.POST("/api/v2/relations/unblock", s.agentAuth("relations:write"), s.requireCompleted, apihandler.UnblockUser)
	h.POST("/api/v2/relations/remark", s.agentAuth("relations:write"), s.requireCompleted, apihandler.UpdateFriendRemark)
	h.GET("/api/v2/agents/me", s.agentAuth("profile:read"), s.requireCompleted, apihandler.GetMe)
	h.GET("/api/v2/agents/me/card", s.agentAuth("profile:read"), s.requireCompleted, agentcardapi.GetMyCard)
	h.GET("/api/v2/agents/me/card/refresh-context", s.agentAuth("onboarding:write"), s.requireCompleted, agentcardapi.GetRefreshContext)
	h.GET("/api/v2/agents/items", s.agentAuth("profile:read"), s.requireCompleted, apihandler.GetMyItems)
	h.DELETE("/api/v2/agents/items/:item_id", s.agentAuth("broadcast:write"), s.requireCompleted, apihandler.DeleteMyItem)
	h.GET("/api/v2/agents/me/settings", s.agentAuth("settings:read"), s.requireCompleted, apihandler.GetMySettings)
	h.PUT("/api/v2/agents/me/settings", middleware.ClientInfoMiddleware(), s.agentAuth("settings:write"), s.requireCompleted, apihandler.PutMySettings)
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

func validActiveIdentityBinding(agentID, principalAgentID int64, identityState, principalStatus string, principalRevokedAt *int64) bool {
	return agentID > 0 && agentID == principalAgentID && identityState == "active" && principalRevokedAt == nil &&
		(principalStatus == "limited" || principalStatus == "active")
}

func (s *Service) setConsoleCookie(c *app.RequestContext, value string, maxAge int) {
	s.setConsoleCookieAtSlot(c, 0, value, maxAge)
}

func (s *Service) setCSRFCookie(c *app.RequestContext, value string, maxAge int) {
	s.setCSRFCookieAtSlot(c, 0, value, maxAge)
}

type agentPrincipal struct {
	SessionID   int64          `gorm:"column:session_id"`
	AgentID     int64          `gorm:"column:agent_id"`
	PrincipalID int64          `gorm:"column:principal_id"`
	Status      string         `gorm:"column:status"`
	Scopes      pq.StringArray `gorm:"column:scopes;type:text[]"`
}

func (s *Service) agentAuth(requiredScope string) app.HandlerFunc {
	return s.agentAuthAny(requiredScope)
}

func (s *Service) agentAuthAny(requiredScopes ...string) app.HandlerFunc {
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
				JOIN agents a ON a.agent_id = p.agent_id
				WHERE cs.access_token_hash = ? AND cs.audience = 'agent_v2'
				  AND cs.revoked_at IS NULL AND cs.expires_at > ? AND cs.access_refresh_required = FALSE
				  AND p.revoked_at IS NULL AND p.status IN ('limited','active')
				  AND a.identity_state = 'active'`, hashString(token), now).
			Scan(&principal).Error
		hasRequiredScope := false
		for _, requiredScope := range requiredScopes {
			if containsScope(principal.Scopes, requiredScope) {
				hasRequiredScope = true
				break
			}
		}
		if err != nil || principal.AgentID == 0 || !hasRequiredScope {
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
	SessionID          string         `gorm:"column:session_id"`
	AgentID            int64          `gorm:"column:agent_id"`
	PrincipalID        int64          `gorm:"column:principal_id"`
	PrincipalAgentID   int64          `gorm:"column:principal_agent_id"`
	PrincipalStatus    string         `gorm:"column:principal_status"`
	PrincipalRevokedAt *int64         `gorm:"column:principal_revoked_at"`
	IdentityState      string         `gorm:"column:identity_state"`
	SecretHash         string         `gorm:"column:session_secret_hash"`
	CSRFSecretHash     string         `gorm:"column:csrf_secret_hash"`
	Scopes             pq.StringArray `gorm:"column:scopes;type:text[]"`
	IdleExpiresAt      int64          `gorm:"column:idle_expires_at"`
	AbsoluteExpiry     int64          `gorm:"column:absolute_expires_at"`
	LastSeenAt         int64          `gorm:"column:last_seen_at"`
	AuthMethod         string         `gorm:"column:auth_method"`
	RecentAuthAt       *int64         `gorm:"column:recent_auth_at"`
}

func (s *Service) consoleAuth(requireCSRF bool) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		if requireCSRF && !validConsoleSameOrigin(string(c.GetHeader("Origin")), string(c.Host()), s.publicURL) {
			fail(c, http.StatusForbidden, "ORIGIN_INVALID", "Console V2 request origin is invalid", nil)
			c.Abort()
			return
		}
		now := time.Now().UnixMilli()
		slot, session, err := s.resolveActiveConsoleSession(c, now)
		if err != nil || session.SessionID == "" {
			if errors.Is(err, errConsoleSessionInvalid) {
				fail(c, http.StatusUnauthorized, "CONSOLE_SESSION_INVALID", "Console V2 session is invalid or expired", nil)
			} else {
				fail(c, http.StatusUnauthorized, "CONSOLE_SESSION_REQUIRED", "Console V2 session is required", nil)
			}
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
			idle := now + int64(consoleIdleTTL/time.Millisecond)
			if idle > session.AbsoluteExpiry {
				idle = session.AbsoluteExpiry
			}
			s.db.Exec(`UPDATE console_v2_sessions SET last_seen_at = ?, idle_expires_at = ?
				WHERE session_id = ? AND last_seen_at = ?`, now, idle, session.SessionID, session.LastSeenAt)
		}
		c.Set("agent_id", session.AgentID)
		c.Set("principal_id", session.PrincipalID)
		c.Set("console_session_id", session.SessionID)
		c.Set("console_session_slot", slot)
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

func (s *Service) requireIncomplete(ctx context.Context, c *app.RequestContext) {
	id, ok := agentID(c)
	if !ok {
		fail(c, http.StatusUnauthorized, "AGENT_AUTH_REQUIRED", "Agent V2 authentication is required", nil)
		c.Abort()
		return
	}
	var state struct {
		State       string `gorm:"column:state"`
		CurrentStep int16  `gorm:"column:current_step"`
	}
	if err := s.db.Raw(`SELECT state, current_step FROM agent_onboarding_v2 WHERE agent_id = ?`, id).Scan(&state).Error; err != nil || state.State == "" {
		fail(c, http.StatusUnauthorized, "AGENT_AUTH_INVALID", "Agent V2 identity is unavailable", nil)
		c.Abort()
		return
	}
	if state.State == "completed" {
		fail(c, http.StatusConflict, "ATTENTION_PREFILL_CLOSED", "Attention Prefill closes when onboarding completes", map[string]interface{}{"current_step": state.CurrentStep})
		c.Abort()
		return
	}
	c.Next(ctx)
}
