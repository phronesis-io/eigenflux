package main

import (
	"context"
	"time"

	"eigenflux_server/kitex_gen/eigenflux/base"
	"eigenflux_server/kitex_gen/eigenflux/profile"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"
	itemdal "eigenflux_server/rpc/item/dal"
	"eigenflux_server/rpc/profile/dal"
)

type ProfileServiceImpl struct {
	agentIDGen interface {
		NextID() (int64, error)
	}
}

func (s *ProfileServiceImpl) RegisterAgent(ctx context.Context, req *profile.RegisterAgentReq) (*profile.RegisterAgentResp, error) {
	logger.Ctx(ctx).Info("RegisterAgent called", "email", req.Email)
	if s.agentIDGen == nil {
		return &profile.RegisterAgentResp{
			BaseResp: &base.BaseResp{Code: 500, Msg: "agent id generator is not initialized"},
		}, nil
	}
	agentID, genErr := s.agentIDGen.NextID()
	if genErr != nil {
		return &profile.RegisterAgentResp{
			BaseResp: &base.BaseResp{Code: 500, Msg: "failed to generate agent id: " + genErr.Error()},
		}, nil
	}

	agent := &dal.Agent{
		AgentID:   agentID,
		Email:     req.Email,
		AgentName: req.GetAgentName(),
		Bio:       req.GetBio(),
	}
	if err := dal.CreateAgent(db.DB, agent); err != nil {
		return &profile.RegisterAgentResp{
			BaseResp: &base.BaseResp{Code: 500, Msg: err.Error()},
		}, nil
	}
	// Create initial agent_profiles record with status=0 (pending)
	ap := &dal.AgentProfile{
		AgentID: agent.AgentID,
		Status:  0,
	}
	dal.CreateAgentProfile(db.DB, ap)

	return &profile.RegisterAgentResp{
		AgentId:  agent.AgentID,
		BaseResp: &base.BaseResp{Code: 0, Msg: "success"},
	}, nil
}

func (s *ProfileServiceImpl) UpdateProfile(ctx context.Context, req *profile.UpdateProfileReq) (*profile.UpdateProfileResp, error) {
	logger.Ctx(ctx).Info("UpdateProfile called", "agentID", req.AgentId)
	// Build update map from provided fields
	updates := make(map[string]interface{})
	bioChanged := false

	if req.AgentName != nil && *req.AgentName != "" {
		updates["agent_name"] = *req.AgentName
	}
	if req.Bio != nil {
		updates["bio"] = *req.Bio
		bioChanged = true
	}

	if len(updates) == 0 {
		return &profile.UpdateProfileResp{
			BaseResp: &base.BaseResp{Code: 400, Msg: "no fields to update"},
		}, nil
	}

	// Check if profile should be marked as completed
	agent, err := dal.GetAgentByID(db.DB, req.AgentId)
	if err != nil {
		return &profile.UpdateProfileResp{
			BaseResp: &base.BaseResp{Code: 404, Msg: "agent not found"},
		}, nil
	}

	// Determine the final values after the update
	finalAgentName := agent.AgentName
	if v, ok := updates["agent_name"]; ok {
		finalAgentName = v.(string)
	}
	finalBio := agent.Bio
	if v, ok := updates["bio"]; ok {
		finalBio = v.(string)
	}

	profileJustCompleted := false
	if agent.ProfileCompletedAt == nil && finalAgentName != "" && finalBio != "" {
		now := time.Now().UnixMilli()
		updates["profile_completed_at"] = now
		profileJustCompleted = true
	}

	if err := dal.UpdateAgentFields(db.DB, req.AgentId, updates); err != nil {
		return &profile.UpdateProfileResp{
			BaseResp: &base.BaseResp{Code: 500, Msg: err.Error()},
		}, nil
	}

	// Reset profile status if bio changed (trigger reprocessing)
	if bioChanged {
		dal.UpdateAgentProfileStatus(db.DB, req.AgentId, 0)
	}

	return &profile.UpdateProfileResp{
		ProfileJustCompleted: &profileJustCompleted,
		BaseResp:             &base.BaseResp{Code: 0, Msg: "success"},
	}, nil
}

func (s *ProfileServiceImpl) GetAgent(ctx context.Context, req *profile.GetAgentReq) (*profile.GetAgentResp, error) {
	logger.Ctx(ctx).Debug("GetAgent called", "agentID", req.AgentId)
	agent, err := dal.GetAgentByID(db.DB, req.AgentId)
	if err != nil {
		return &profile.GetAgentResp{
			BaseResp: &base.BaseResp{Code: 404, Msg: "agent not found"},
		}, nil
	}

	// Get agent profile for country
	agentProfile, _ := dal.GetAgentProfile(db.DB, req.AgentId)
	var country string
	if agentProfile != nil {
		country = agentProfile.Country
	}

	// Get influence metrics
	influence, err := itemdal.GetAgentInfluenceMetrics(db.DB, req.AgentId)
	if err != nil {
		// If error, return zero metrics
		influence = &itemdal.InfluenceMetrics{
			TotalItems:    0,
			TotalConsumed: 0,
			TotalScored1:  0,
			TotalScored2:  0,
		}
	}

	resp := &profile.GetAgentResp{
		Agent: &profile.Agent{
			Id:        agent.AgentID,
			Email:     agent.Email,
			AgentName: agent.AgentName,
			Bio:       agent.Bio,
			CreatedAt: agent.CreatedAt,
			UpdatedAt: agent.UpdatedAt,
			Country:   &country,
		},
		Influence: &profile.InfluenceMetrics{
			TotalItems:    influence.TotalItems,
			TotalConsumed: influence.TotalConsumed,
			TotalScored_1: influence.TotalScored1,
			TotalScored_2: influence.TotalScored2,
		},
		BaseResp: &base.BaseResp{Code: 0, Msg: "success"},
	}

	return resp, nil
}

func (s *ProfileServiceImpl) MatchAgentsByKeywords(ctx context.Context, req *profile.MatchAgentsByKeywordsReq) (*profile.MatchAgentsByKeywordsResp, error) {
	logger.Ctx(ctx).Debug("MatchAgentsByKeywords called", "keywords", req.Keywords)
	if len(req.Keywords) == 0 {
		return &profile.MatchAgentsByKeywordsResp{
			AgentIds: []int64{},
			BaseResp: &base.BaseResp{Code: 0, Msg: "success"},
		}, nil
	}

	// Set default limit if not provided
	limit := 100
	if req.Limit != nil && *req.Limit > 0 {
		limit = int(*req.Limit)
	}

	// Call DAL function to match agents
	agentIDs, err := dal.MatchAgentsByKeywords(db.DB, req.Keywords, req.ExcludeAgentId, limit)
	if err != nil {
		return &profile.MatchAgentsByKeywordsResp{
			AgentIds: []int64{},
			BaseResp: &base.BaseResp{Code: 500, Msg: err.Error()},
		}, nil
	}

	return &profile.MatchAgentsByKeywordsResp{
		AgentIds: agentIDs,
		BaseResp: &base.BaseResp{Code: 0, Msg: "success"},
	}, nil
}
