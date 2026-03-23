package dal

import (
	"console.eigenflux.ai/internal/model"

	"gorm.io/gorm"
)

type AgentWithProfile struct {
	model.Agent
	ProfileStatus   *int16
	ProfileKeywords *string
}

type ListAgentsParams struct {
	Page      int32
	PageSize  int32
	Email     *string
	AgentName *string
}

func ListAgents(db *gorm.DB, params ListAgentsParams) ([]AgentWithProfile, int64, error) {
	var agents []AgentWithProfile
	var total int64

	query := db.Table("agents").
		Select("agents.*, agent_profiles.status as profile_status, agent_profiles.keywords as profile_keywords").
		Joins("LEFT JOIN agent_profiles ON agents.agent_id = agent_profiles.agent_id")

	if params.Email != nil && *params.Email != "" {
		query = query.Where("agents.email = ?", *params.Email)
	}
	if params.AgentName != nil && *params.AgentName != "" {
		query = query.Where("agents.agent_name ILIKE ?", "%"+*params.AgentName+"%")
	}

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	offset := (params.Page - 1) * params.PageSize
	if err := query.Offset(int(offset)).Limit(int(params.PageSize)).Order("agents.agent_id DESC").Find(&agents).Error; err != nil {
		return nil, 0, err
	}

	return agents, total, nil
}

type ItemWithProcessed struct {
	model.RawItem
	Status           *int16
	Summary          *string
	BroadcastType    *string
	Domains          *string
	Keywords         *string
	ExpireTime       *string
	Geo              *string
	SourceType       *string
	ExpectedResponse *string
	GroupID          *int64
	UpdatedAt        *int64
}

type ListItemsParams struct {
	Page     int32
	PageSize int32
	Status   *int32
	Keyword  *string
	Title    *string
}

func ListItems(db *gorm.DB, params ListItemsParams) ([]ItemWithProcessed, int64, error) {
	var items []ItemWithProcessed
	var total int64

	query := db.Table("raw_items").
		Select("raw_items.*, processed_items.status, processed_items.summary, processed_items.broadcast_type, processed_items.domains, processed_items.keywords, processed_items.expire_time, processed_items.geo, processed_items.source_type, processed_items.expected_response, processed_items.group_id, processed_items.updated_at").
		Joins("LEFT JOIN processed_items ON raw_items.item_id = processed_items.item_id")

	if params.Status != nil {
		query = query.Where("processed_items.status = ?", *params.Status)
	}
	if params.Keyword != nil && *params.Keyword != "" {
		query = query.Where("processed_items.keywords ILIKE ?", "%"+*params.Keyword+"%")
	}
	if params.Title != nil && *params.Title != "" {
		query = query.Where("(raw_items.raw_content ILIKE ? OR processed_items.summary ILIKE ?)",
			"%"+*params.Title+"%", "%"+*params.Title+"%")
	}

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	offset := (params.Page - 1) * params.PageSize
	if err := query.Offset(int(offset)).Limit(int(params.PageSize)).Order("raw_items.item_id DESC").Find(&items).Error; err != nil {
		return nil, 0, err
	}

	return items, total, nil
}

func ListItemsByIDs(db *gorm.DB, itemIDs []int64) ([]ItemWithProcessed, error) {
	if len(itemIDs) == 0 {
		return []ItemWithProcessed{}, nil
	}

	var items []ItemWithProcessed
	err := db.Table("raw_items").
		Select("raw_items.*, processed_items.status, processed_items.summary, processed_items.broadcast_type, processed_items.domains, processed_items.keywords, processed_items.expire_time, processed_items.geo, processed_items.source_type, processed_items.expected_response, processed_items.group_id, processed_items.updated_at").
		Joins("LEFT JOIN processed_items ON raw_items.item_id = processed_items.item_id").
		Where("raw_items.item_id IN ?", itemIDs).
		Order("raw_items.item_id DESC").
		Find(&items).Error
	if err != nil {
		return nil, err
	}

	return items, nil
}
