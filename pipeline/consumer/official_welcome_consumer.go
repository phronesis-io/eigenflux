package consumer

import (
	"context"
	"strconv"
	"sync"

	"gorm.io/gorm"

	"eigenflux_server/kitex_gen/eigenflux/pm"
	"eigenflux_server/kitex_gen/eigenflux/pm/pmservice"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/mq"
	pmdal "eigenflux_server/rpc/pm/dal"
	"eigenflux_server/rpc/pm/relations"
	profiledal "eigenflux_server/rpc/profile/dal"
)

const (
	officialWelcomeStream       = "stream:profile:update"
	officialWelcomeGroup        = "cg:official:welcome"
	officialWelcomeConsumerName = "official-welcome-worker-1"
	officialWelcomeMetricsLabel = "official:welcome"
	officialWelcomeRemark       = "EigenFlux 官方"
)

func officialWelcomedKey(agentID int64) string {
	return "official:welcomed:" + strconv.FormatInt(agentID, 10)
}

// OfficialWelcomeConsumer reacts to profile-completion events: it makes the new
// agent and the official account friends, then sends a one-time welcome PM as
// the official account. It subscribes to the same stream:profile:update as
// ProfileConsumer but in its own consumer group, so the two are independent.
type OfficialWelcomeConsumer struct {
	pmClient       pmservice.Client
	welcomeMessage string
	officialEmail  string

	runner *StreamConsumer

	mu         sync.Mutex
	officialID int64
}

func NewOfficialWelcomeConsumer(cfg *config.Config, pmClient pmservice.Client) *OfficialWelcomeConsumer {
	c := &OfficialWelcomeConsumer{
		pmClient:       pmClient,
		welcomeMessage: cfg.OfficialWelcomeMessage,
		officialEmail:  cfg.OfficialAgentEmail,
	}
	c.runner = &StreamConsumer{
		Name:         "OfficialWelcomeConsumer",
		Stream:       officialWelcomeStream,
		Group:        officialWelcomeGroup,
		ConsumerName: officialWelcomeConsumerName,
		MetricsLabel: officialWelcomeMetricsLabel,
		Workers:      4,
		MaxRetries:   3,
		Handle:       c.handle,
	}
	return c
}

func (c *OfficialWelcomeConsumer) Start(ctx context.Context) { c.runner.Run(ctx) }

// resolveOfficialID looks up the official account's agent_id by email and caches
// it once found. Returns 0 when the official account has not been provisioned
// yet, in which case the consumer simply does nothing (retries on later events).
func (c *OfficialWelcomeConsumer) resolveOfficialID() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.officialID != 0 {
		return c.officialID
	}
	agent, err := profiledal.GetAgentByEmail(db.DB, c.officialEmail)
	if err != nil || agent == nil {
		return 0
	}
	c.officialID = agent.AgentID
	return c.officialID
}

func (c *OfficialWelcomeConsumer) handle(ctx context.Context, _ string, values map[string]any) HandleResult {
	agentIDStr, ok := values["agent_id"].(string)
	if !ok {
		logger.Default().Warn("OfficialWelcomeConsumer missing agent_id")
		return HandleFailure
	}
	agentID, err := strconv.ParseInt(agentIDStr, 10, 64)
	if err != nil {
		logger.Default().Warn("OfficialWelcomeConsumer invalid agent_id", "agentID", agentIDStr)
		return HandleFailure
	}

	officialID := c.resolveOfficialID()
	if officialID == 0 || agentID == officialID {
		return HandleSuccess
	}

	// Only welcome once the profile is actually complete. profile:update also
	// fires on later bio edits, which must not re-trigger the welcome.
	agent, err := profiledal.GetAgentByID(db.DB, agentID)
	if err != nil {
		logger.Default().Warn("OfficialWelcomeConsumer agent not found", "agentID", agentID, "err", err)
		return HandleRetry
	}
	if agent.ProfileCompletedAt == nil {
		return HandleSuccess
	}

	// Dedup gate: welcome each agent at most once. Released on transient failure
	// below so the message can be retried.
	gateKey := officialWelcomedKey(agentID)
	acquired, err := mq.RDB.SetNX(ctx, gateKey, "1", 0).Result()
	if err != nil {
		logger.Default().Warn("OfficialWelcomeConsumer dedup gate failed", "agentID", agentID, "err", err)
		return HandleRetry
	}
	if !acquired {
		return HandleSuccess // already welcomed
	}

	// Respect the user's strongest rejection signal: if they blocked the
	// official account, do not friend or message them.
	if blocked, err := pmdal.IsBlocked(db.DB, agentID, officialID); err == nil && blocked {
		logger.Default().Info("OfficialWelcomeConsumer skip blocked user", "agentID", agentID)
		return HandleSuccess
	}

	if err := c.ensureFriendship(ctx, officialID, agentID); err != nil {
		_ = mq.RDB.Del(ctx, gateKey).Err()
		logger.Default().Warn("OfficialWelcomeConsumer ensure friendship failed", "agentID", agentID, "err", err)
		return HandleRetry
	}

	resp, err := c.pmClient.SendPM(ctx, &pm.SendPMReq{
		SenderId:   officialID,
		ReceiverId: agentID,
		Content:    c.welcomeMessage,
	})
	if err != nil {
		_ = mq.RDB.Del(ctx, gateKey).Err()
		logger.Default().Warn("OfficialWelcomeConsumer send welcome failed", "agentID", agentID, "err", err)
		return HandleRetry
	}
	// SendPM signals application-level rejection (e.g. not-friends 403) via
	// BaseResp.Code, not a transport error.
	if resp.GetBaseResp() != nil && resp.GetBaseResp().GetCode() != 0 {
		_ = mq.RDB.Del(ctx, gateKey).Err()
		logger.Default().Warn("OfficialWelcomeConsumer welcome rejected", "agentID", agentID,
			"code", resp.GetBaseResp().GetCode(), "msg", resp.GetBaseResp().GetMsg())
		return HandleRetry
	}

	logger.Default().Info("OfficialWelcomeConsumer welcomed new agent", "agentID", agentID, "officialID", officialID)
	return HandleSuccess
}

// ensureFriendship makes officialID and userID friends if they are not already.
// It is idempotent and accepts any pending friend request between them so the
// friend_requests table stays consistent with the relation it creates.
func (c *OfficialWelcomeConsumer) ensureFriendship(ctx context.Context, officialID, userID int64) error {
	created := false
	err := db.DB.Transaction(func(tx *gorm.DB) error {
		if err := pmdal.LockRelationPair(tx, officialID, userID); err != nil {
			return err
		}
		isFriend, err := pmdal.IsFriend(tx, officialID, userID)
		if err != nil {
			return err
		}
		if isFriend {
			return nil
		}
		acceptPendingRequest(tx, userID, officialID)
		acceptPendingRequest(tx, officialID, userID)
		if err := pmdal.CreateFriendRelation(tx, officialID, userID, officialWelcomeRemark, ""); err != nil {
			return err
		}
		created = true
		return nil
	})
	if err != nil {
		return err
	}
	if created {
		// PMService.SendPM checks friendship from a Redis friend-set cache; a
		// relation created directly via the DAL must invalidate it (as the normal
		// accept path does) or the welcome PM is rejected as "not friends".
		_ = relations.InvalidateFriendCache(ctx, mq.RDB, officialID)
		_ = relations.InvalidateFriendCache(ctx, mq.RDB, userID)
	}
	return nil
}

func acceptPendingRequest(tx *gorm.DB, fromUID, toUID int64) {
	req, err := pmdal.GetFriendRequestBetweenForUpdate(tx, fromUID, toUID)
	if err != nil || req == nil {
		return
	}
	_, _ = pmdal.UpdateRequestStatusIfPending(tx, req.ID, pmdal.RequestStatusAccepted)
}
