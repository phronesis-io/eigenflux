package main

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"eigenflux_server/kitex_gen/eigenflux/base"
	"eigenflux_server/kitex_gen/eigenflux/profile"
	"eigenflux_server/pkg/agentcard"
	"eigenflux_server/pkg/agentidentity"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/reqinfo"
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
	err := db.DB.Transaction(func(tx *gorm.DB) error {
		if err := dal.CreateAgent(tx, agent); err != nil {
			return err
		}
		return dal.CreateAgentProfile(tx, &dal.AgentProfile{AgentID: agent.AgentID, Status: 0})
	})
	if err != nil {
		return &profile.RegisterAgentResp{
			BaseResp: &base.BaseResp{Code: 500, Msg: err.Error()},
		}, nil
	}
	agentcard.PublishRebuild(ctx, agent.AgentID, "agent_registered")
	displayName := agentidentity.DisplayName(agent.AgentName, agent.ShortID)

	return &profile.RegisterAgentResp{
		AgentId:     agent.AgentID,
		ShortId:     &agent.ShortID,
		DisplayName: &displayName,
		BaseResp:    &base.BaseResp{Code: 0, Msg: "success"},
	}, nil
}

func (s *ProfileServiceImpl) UpdateProfile(ctx context.Context, req *profile.UpdateProfileReq) (*profile.UpdateProfileResp, error) {
	logger.Ctx(ctx).Info("UpdateProfile called", "agentID", req.AgentId)
	if (req.AgentName == nil || *req.AgentName == "") && req.Bio == nil {
		return &profile.UpdateProfileResp{
			BaseResp: &base.BaseResp{Code: 400, Msg: "no fields to update"},
		}, nil
	}
	for field, value := range map[string]*string{
		"agent_name":        req.AgentName,
		"agent_description": req.Bio,
	} {
		if value == nil {
			continue
		}
		spec, _ := agentcard.LookupField(field)
		raw, _ := json.Marshal(*value)
		normalized, validationErr := agentcard.ValidateValue(spec, raw)
		if validationErr == nil {
			validationErr = agentcard.ValidatePublicContent(spec, normalized)
		}
		if validationErr != nil {
			return &profile.UpdateProfileResp{BaseResp: &base.BaseResp{Code: 422, Msg: validationErr.Error()}}, nil
		}
	}

	// The write, its audit records and the optimistic-lock version bump commit
	// atomically, so field-level writers (PUT /agents/me/profile/fields) racing
	// this legacy path get a clean 409 instead of a silent overwrite.
	prov := reqinfo.BioProvenanceFromContext(ctx)
	profileJustCompleted := false
	bioChanged := false
	nameChanged := false
	resultMsg := "no_change"
	err := db.DB.Transaction(func(tx *gorm.DB) error {
		agent, err := dal.GetAgentByIDForUpdate(tx, req.AgentId)
		if err != nil {
			return err
		}
		updates := make(map[string]interface{})
		finalAgentName := agent.AgentName
		if req.AgentName != nil && *req.AgentName != "" {
			finalAgentName = *req.AgentName
			if finalAgentName != agent.AgentName {
				updates["agent_name"] = finalAgentName
				updates["agent_name_en"] = ""
				nameChanged = true
			}
		}
		finalBio := agent.Bio
		if req.Bio != nil {
			finalBio = *req.Bio
			if finalBio != agent.Bio {
				updates["bio"] = finalBio
				bioChanged = true
			}
		}
		if agent.ProfileCompletedAt == nil && finalAgentName != "" && finalBio != "" {
			updates["profile_completed_at"] = time.Now().UnixMilli()
			profileJustCompleted = true
		}
		if len(updates) == 0 {
			return nil
		}
		if err := dal.UpdateAgentFields(tx, req.AgentId, updates); err != nil {
			return err
		}
		if err := dal.EnsureAgentProfileRow(tx, req.AgentId); err != nil {
			return err
		}
		newVersion, err := dal.BumpProfileVersion(tx, req.AgentId)
		if err != nil {
			return err
		}

		// Record bio history only on a real change (not a no-op re-submit). This is
		// both the daily bio history and the authoritative layer-2 telemetry that an
		// automated refresh actually took effect. prov.Source / prov.Note are the
		// agent's self-reported provenance (memory/session/broadcast), empty for a
		// manual update.
		if bioChanged {
			if herr := dal.InsertBioHistory(tx, req.AgentId, agent.Bio, finalBio, prov.Source, prov.Note); herr != nil {
				return herr
			}
			logger.Ctx(ctx).Info("bio_history_recorded",
				"agentID", req.AgentId,
				"source", prov.Source,
				"note_len", len(prov.Note),
				"prev_len", len(agent.Bio),
				"new_len", len(finalBio),
			)
		}

		// Generic change event mirroring what the field-level endpoint writes,
		// so refresh-context sees legacy-path edits too. bio maps to the Card
		// field name agent_description.
		paths := make([]string, 0, 2)
		prevVals := map[string]string{}
		newVals := map[string]string{}
		if nameChanged {
			paths = append(paths, "agent_name")
			prevVals["agent_name"] = agent.AgentName
			newVals["agent_name"] = finalAgentName
		}
		if bioChanged {
			paths = append(paths, "agent_description")
			prevVals["agent_description"] = agent.Bio
			newVals["agent_description"] = finalBio
		}
		if len(paths) > 0 {
			pathsJSON, _ := json.Marshal(paths)
			prevJSON, _ := json.Marshal(prevVals)
			newJSON, _ := json.Marshal(newVals)
			if err := dal.InsertProfileChangeEvent(tx, &dal.ProfileChangeEvent{
				AgentID:        req.AgentId,
				SourceVersion:  newVersion,
				ActorType:      "agent",
				ActorID:        strconv.FormatInt(req.AgentId, 10),
				Source:         prov.Source,
				Reason:         prov.Note,
				ChangedPaths:   string(pathsJSON),
				PreviousValues: string(prevJSON),
				NewValues:      string(newJSON),
			}); err != nil {
				return err
			}
		}
		switch {
		case nameChanged && bioChanged:
			resultMsg = "name_and_bio_changed"
		case bioChanged:
			resultMsg = "bio_changed"
		case nameChanged:
			resultMsg = "name_changed"
		default:
			resultMsg = "profile_metadata_only"
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return &profile.UpdateProfileResp{BaseResp: &base.BaseResp{Code: 404, Msg: "agent not found"}}, nil
		}
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
		BaseResp:             &base.BaseResp{Code: 0, Msg: resultMsg},
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

	// Get agent profile for country and keywords
	agentProfile, _ := dal.GetAgentProfile(db.DB, req.AgentId)
	var country string
	keywords := []string{}
	// last_updated reflects whichever backing table changed most recently.
	updatedAt := agent.UpdatedAt
	if agentProfile != nil {
		country = agentProfile.Country
		if agentProfile.Keywords != "" {
			keywords = strings.Split(agentProfile.Keywords, ",")
		}
		if agentProfile.UpdatedAt > updatedAt {
			updatedAt = agentProfile.UpdatedAt
		}
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

	displayName := agentidentity.DisplayName(agent.AgentName, agent.ShortID)
	resp := &profile.GetAgentResp{
		Agent: &profile.Agent{
			Id:          agent.AgentID,
			Email:       agent.Email,
			AgentName:   agent.AgentName,
			Bio:         agent.Bio,
			CreatedAt:   agent.CreatedAt,
			UpdatedAt:   updatedAt,
			Country:     &country,
			Keywords:    keywords,
			ShortId:     &agent.ShortID,
			DisplayName: &displayName,
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
