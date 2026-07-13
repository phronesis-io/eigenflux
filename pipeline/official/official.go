// Package official centralizes the official-account behaviors shared by the
// pipeline consumers (welcome, first-broadcast reply, inbox chat) and the cron
// senders (feed rescue, trending): resolving the account, gating, cooldowns,
// LLM message generation against the official persona prompt, sending DMs, and
// rate-limiting proactive/reactive LLM replies.
package official

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	apidal "eigenflux_server/api/dal"
	"eigenflux_server/kitex_gen/eigenflux/pm"
	"eigenflux_server/kitex_gen/eigenflux/pm/pmservice"
	"eigenflux_server/pipeline/llm"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/mq"
	pmdal "eigenflux_server/rpc/pm/dal"
	profiledal "eigenflux_server/rpc/profile/dal"
)

// Sender is the shared entry point for acting as the official account.
type Sender struct {
	pm      pmservice.Client
	llm     *llm.Client
	prompts *llm.PromptRegistry
	cfg     *config.Config

	mu         sync.Mutex
	officialID int64
}

func NewSender(cfg *config.Config, pmClient pmservice.Client, llmClient *llm.Client, prompts *llm.PromptRegistry) *Sender {
	return &Sender{pm: pmClient, llm: llmClient, prompts: prompts, cfg: cfg}
}

// ResolveOfficialID looks up the official account's agent_id by email and caches
// it. Returns 0 when not provisioned (callers then no-op).
func (s *Sender) ResolveOfficialID() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.officialID != 0 {
		return s.officialID
	}
	agent, err := profiledal.GetAgentByEmail(db.DB, s.cfg.OfficialAgentEmail)
	if err != nil || agent == nil {
		return 0
	}
	s.officialID = agent.AgentID
	return s.officialID
}

// PassesGate reports whether the official account may message target: they must
// be friends and not have opted out.
func (s *Sender) PassesGate(officialID, targetID int64, targetEmail string) bool {
	if friend, err := pmdal.IsFriend(db.DB, officialID, targetID); err != nil || !friend {
		return false
	}
	if cfgs, err := apidal.GetSettings(db.DB, targetID); err == nil && cfgs.OfficialPMOptout {
		return false
	}
	return true
}

// ChatGate is like PassesGate but for user-initiated chat (#2): it requires
// friendship but ignores the proactive-PM opt-out — a user who DMs the official
// account wants a reply regardless of having muted proactive pushes.
func (s *Sender) ChatGate(officialID, userID int64, userEmail string) bool {
	if friend, err := pmdal.IsFriend(db.DB, officialID, userID); err != nil || !friend {
		return false
	}
	return true
}

// CooldownAcquire SETNX-gates a per-feature, per-user cooldown. Returns true
// when the action may proceed (not currently on cooldown).
func (s *Sender) CooldownAcquire(ctx context.Context, kind string, targetID int64, ttl time.Duration) bool {
	ok, err := mq.RDB.SetNX(ctx, fmt.Sprintf("official:pm:cooldown:%s:%d", kind, targetID), "1", ttl).Result()
	if err != nil {
		return false
	}
	return ok
}

func (s *Sender) CooldownRelease(ctx context.Context, kind string, targetID int64) {
	_ = mq.RDB.Del(ctx, fmt.Sprintf("official:pm:cooldown:%s:%d", kind, targetID)).Err()
}

// AllowReply enforces the LLM-reply thresholds for reactive features (#2 chat,
// #3 first-broadcast reply): per-user-per-minute, per-user-per-day, and a global
// per-minute cap. Returns false (caller stays silent) when any limit is hit.
func (s *Sender) AllowReply(ctx context.Context, userID int64) bool {
	perMin := s.cfg.OfficialChatPerUserPerMin
	if perMin <= 0 {
		perMin = 1
	}
	daily := s.cfg.OfficialChatDailyPerUser
	if daily <= 0 {
		daily = 50
	}
	globalPerMin := s.cfg.OfficialChatGlobalPerMin
	if globalPerMin <= 0 {
		globalPerMin = 60
	}
	// Per-user gates first so a single spammer rarely consumes the global slot.
	if !allowWindow(ctx, fmt.Sprintf("official:llm:umin:%d", userID), time.Minute, perMin) {
		return false
	}
	if !allowWindow(ctx, fmt.Sprintf("official:llm:uday:%d", userID), 26*time.Hour, daily) {
		return false
	}
	if !allowWindow(ctx, "official:llm:global", time.Minute, globalPerMin) {
		return false
	}
	return true
}

// allowWindow is a fixed-window counter: INCR the key (setting TTL on first hit)
// and report whether the count is still within limit.
func allowWindow(ctx context.Context, key string, ttl time.Duration, limit int) bool {
	n, err := mq.RDB.Incr(ctx, key).Result()
	if err != nil {
		return false
	}
	if n == 1 {
		_ = mq.RDB.Expire(ctx, key, ttl).Err()
	}
	return n <= int64(limit)
}

// LangOf returns the member's dashboard language preference
// (agent_settings.lang, "zh"/"en"), or "" when they never chose one.
func LangOf(agentID int64) string {
	s, err := apidal.GetSettings(db.DB, agentID)
	if err != nil {
		return ""
	}
	if s.Lang == "zh" || s.Lang == "en" {
		return s.Lang
	}
	return ""
}

// LangDirective returns a prompt suffix pinning the reply language to the
// member's dashboard preference. Empty when the member never chose a language,
// so callers keep their guess-from-content wording as the fallback.
func LangDirective(agentID int64) string {
	return DirectiveForLang(LangOf(agentID))
}

// DirectiveForLang is the prompt suffix for an already-resolved preference.
func DirectiveForLang(lang string) string {
	switch lang {
	case "zh":
		return " The member's dashboard language is Chinese: write the whole message in natural, native-quality Simplified Chinese."
	case "en":
		return " The member's dashboard language is English: write the whole message in English."
	}
	return ""
}

// Generate renders the official persona prompt with a task-specific instruction
// and returns the model's message text.
func (s *Sender) Generate(ctx context.Context, task string) (string, error) {
	prompt, err := s.prompts.Render("official", map[string]any{"Task": task})
	if err != nil {
		return "", fmt.Errorf("render official prompt: %w", err)
	}
	text, err := s.llm.CallText(ctx, prompt, "official")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(text), nil
}

// Send delivers content as the official account in a friend conversation.
// Returns true on confirmed delivery (transport ok AND BaseResp.Code == 0).
func (s *Sender) Send(ctx context.Context, officialID, targetID int64, content string) bool {
	return s.sendPM(ctx, &pm.SendPMReq{SenderId: officialID, ReceiverId: targetID, Content: content})
}

// SendInConversation replies within an existing conversation (e.g. a broadcast
// item conversation) by conv_id.
func (s *Sender) SendInConversation(ctx context.Context, officialID, convID int64, content string) bool {
	return s.sendPM(ctx, &pm.SendPMReq{SenderId: officialID, ConvId: &convID, Content: content})
}

// SendOnItem opens/uses the broadcast conversation for an item (item-originated).
func (s *Sender) SendOnItem(ctx context.Context, officialID, itemID int64, content string) bool {
	return s.sendPM(ctx, &pm.SendPMReq{SenderId: officialID, ItemId: &itemID, Content: content})
}

func (s *Sender) sendPM(ctx context.Context, req *pm.SendPMReq) bool {
	resp, err := s.pm.SendPM(ctx, req)
	if err != nil {
		logger.Default().Warn("official PM send failed", "receiverID", req.ReceiverId, "convID", req.ConvId, "itemID", req.ItemId, "err", err)
		return false
	}
	if resp.GetBaseResp() != nil && resp.GetBaseResp().GetCode() != 0 {
		logger.Default().Warn("official PM rejected", "code", resp.GetBaseResp().GetCode(), "msg", resp.GetBaseResp().GetMsg())
		return false
	}
	return true
}
