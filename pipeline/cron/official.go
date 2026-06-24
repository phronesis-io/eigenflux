package main

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

// officialCtx bundles the shared dependencies and gating the official-account
// PM crons (#4 feed rescue, #5 trending) use to send DMs as the official
// account. The official account is resolved by email and cached.
type officialCtx struct {
	pm           pmservice.Client
	llm          *llm.Client
	prompts      *llm.PromptRegistry
	cfg          *config.Config
	whitelist    map[string]struct{}
	testSuffixes []string // e.g. @eftestbot.com — always pass the whitelist gate

	mu         sync.Mutex
	officialID int64
}

func newOfficialCtx(cfg *config.Config, pmClient pmservice.Client, llmClient *llm.Client, prompts *llm.PromptRegistry) *officialCtx {
	wl := map[string]struct{}{}
	for _, e := range cfg.OfficialPMWhitelist {
		if n := strings.ToLower(strings.TrimSpace(e)); n != "" {
			wl[n] = struct{}{}
		}
	}
	if len(wl) == 0 {
		wl = nil
	}
	return &officialCtx{pm: pmClient, llm: llmClient, prompts: prompts, cfg: cfg, whitelist: wl, testSuffixes: cfg.OfficialTestEmailSuffixes}
}

// resolveOfficialID looks up the official account's agent_id by email and caches
// it. Returns 0 when not provisioned (callers then no-op).
func (o *officialCtx) resolveOfficialID() int64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.officialID != 0 {
		return o.officialID
	}
	agent, err := profiledal.GetAgentByEmail(db.DB, o.cfg.OfficialAgentEmail)
	if err != nil || agent == nil {
		return 0
	}
	o.officialID = agent.AgentID
	return o.officialID
}

// passesGate reports whether the official account may proactively DM target:
// they must be friends, pass the staged-rollout whitelist (when set), and not
// have opted out. Cooldown is checked separately (per feature).
func (o *officialCtx) passesGate(officialID, targetID int64, targetEmail string) bool {
	if friend, err := pmdal.IsFriend(db.DB, officialID, targetID); err != nil || !friend {
		return false
	}
	if o.whitelist != nil && !config.EmailMatchesAnySuffix(targetEmail, o.testSuffixes) {
		if _, ok := o.whitelist[strings.ToLower(strings.TrimSpace(targetEmail))]; !ok {
			return false
		}
	}
	if s, err := apidal.GetSettings(db.DB, targetID); err == nil && s.OfficialPMOptout {
		return false
	}
	return true
}

// cooldownAcquire SETNX-gates a per-feature, per-user cooldown. Returns true
// when the send may proceed (not currently on cooldown).
func (o *officialCtx) cooldownAcquire(ctx context.Context, kind string, targetID int64, ttl time.Duration) bool {
	key := fmt.Sprintf("official:pm:cooldown:%s:%d", kind, targetID)
	ok, err := mq.RDB.SetNX(ctx, key, "1", ttl).Result()
	if err != nil {
		return false
	}
	return ok
}

func cooldownRelease(ctx context.Context, kind string, targetID int64) {
	_ = mq.RDB.Del(ctx, fmt.Sprintf("official:pm:cooldown:%s:%d", kind, targetID)).Err()
}

// generate renders the official persona prompt with a task-specific instruction
// and returns the model's message text.
func (o *officialCtx) generate(ctx context.Context, task string) (string, error) {
	prompt, err := o.prompts.Render("official", map[string]any{"Task": task})
	if err != nil {
		return "", fmt.Errorf("render official prompt: %w", err)
	}
	text, err := o.llm.CallText(ctx, prompt, "official")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(text), nil
}

// send delivers content as the official account over the friend conversation.
// Returns true on confirmed delivery (transport ok AND BaseResp.Code == 0).
func (o *officialCtx) send(ctx context.Context, officialID, targetID int64, content string) bool {
	resp, err := o.pm.SendPM(ctx, &pm.SendPMReq{SenderId: officialID, ReceiverId: targetID, Content: content})
	if err != nil {
		logger.Default().Warn("official PM send failed", "targetID", targetID, "err", err)
		return false
	}
	if resp.GetBaseResp() != nil && resp.GetBaseResp().GetCode() != 0 {
		logger.Default().Warn("official PM rejected", "targetID", targetID,
			"code", resp.GetBaseResp().GetCode(), "msg", resp.GetBaseResp().GetMsg())
		return false
	}
	return true
}
