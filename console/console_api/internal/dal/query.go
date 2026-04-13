package dal

import (
	"errors"
	"strings"
	"time"

	"console.eigenflux.ai/internal/model"

	"gorm.io/gorm"
)

var ErrAgentNotFound = errors.New("agent not found")
var ErrItemNotFound = errors.New("item not found")

var likePatternEscaper = strings.NewReplacer(
	"\\", "\\\\",
	"%", "\\%",
	"_", "\\_",
)

func escapeLikePattern(input string) string {
	return likePatternEscaper.Replace(input)
}

func ilikeContainsPattern(input string) string {
	return "%" + escapeLikePattern(input) + "%"
}

func ilikeSuffixPattern(input string) string {
	return "%" + escapeLikePattern(input)
}

type AgentWithProfile struct {
	model.Agent
	ProfileStatus   *int16
	ProfileKeywords *string
}

type ListAgentsParams struct {
	Page            int32
	PageSize        int32
	Email           *string
	AgentName       *string
	AgentID         *int64
	ProfileStatus   *int32
	ProfileKeywords *string
}

func ListAgents(db *gorm.DB, params ListAgentsParams) ([]AgentWithProfile, int64, error) {
	var agents []AgentWithProfile
	var total int64

	query := db.Table("agents").
		Select("agents.*, agent_profiles.status as profile_status, agent_profiles.keywords as profile_keywords").
		Joins("LEFT JOIN agent_profiles ON agents.agent_id = agent_profiles.agent_id")

	if params.Email != nil && *params.Email != "" {
		query = query.Where("agents.email ILIKE ? ESCAPE '\\'", ilikeContainsPattern(*params.Email))
	}
	if params.AgentName != nil && *params.AgentName != "" {
		query = query.Where("agents.agent_name ILIKE ? ESCAPE '\\'", ilikeContainsPattern(*params.AgentName))
	}
	if params.AgentID != nil {
		query = query.Where("agents.agent_id = ?", *params.AgentID)
	}
	if params.ProfileStatus != nil {
		query = query.Where("agent_profiles.status = ?", *params.ProfileStatus)
	}
	if params.ProfileKeywords != nil && *params.ProfileKeywords != "" {
		query = query.Where("agent_profiles.keywords ILIKE ? ESCAPE '\\'", ilikeContainsPattern(*params.ProfileKeywords))
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

type UpdateAgentParams struct {
	ProfileKeywords *[]string // nil = not updating
}

// UpdateAgent applies partial updates to an agent.
// Returns the refreshed AgentWithProfile.
func UpdateAgent(db *gorm.DB, agentID int64, params UpdateAgentParams) (*AgentWithProfile, error) {
	// Verify agent exists
	var count int64
	if err := db.Table("agents").Where("agent_id = ?", agentID).Count(&count).Error; err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, ErrAgentNotFound
	}

	now := time.Now().UnixMilli()

	// Update agent_profiles fields (keywords etc.)
	if params.ProfileKeywords != nil {
		joined := strings.Join(*params.ProfileKeywords, ",")
		result := db.Exec(`
			INSERT INTO agent_profiles (agent_id, keywords, updated_at)
			VALUES (?, ?, ?)
			ON CONFLICT (agent_id) DO UPDATE SET keywords = EXCLUDED.keywords, updated_at = EXCLUDED.updated_at
		`, agentID, joined, now)
		if result.Error != nil {
			return nil, result.Error
		}
	}

	// Re-read full agent data
	var agent AgentWithProfile
	err := db.Table("agents").
		Select("agents.*, agent_profiles.status as profile_status, agent_profiles.keywords as profile_keywords").
		Joins("LEFT JOIN agent_profiles ON agents.agent_id = agent_profiles.agent_id").
		Where("agents.agent_id = ?", agentID).
		First(&agent).Error
	if err != nil {
		return nil, err
	}
	return &agent, nil
}

func GetAgentByID(db *gorm.DB, agentID int64) (*AgentWithProfile, error) {
	var agent AgentWithProfile
	err := db.Table("agents").
		Select("agents.*, agent_profiles.status as profile_status, agent_profiles.keywords as profile_keywords").
		Joins("LEFT JOIN agent_profiles ON agents.agent_id = agent_profiles.agent_id").
		Where("agents.agent_id = ?", agentID).
		First(&agent).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrAgentNotFound
		}
		return nil, err
	}
	return &agent, nil
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
	Page                 int32
	PageSize             int32
	Status               *int32
	Keyword              *string
	Title                *string
	ExcludeEmailSuffixes []string
	IncludeEmailSuffixes []string
	ItemID               *int64
	GroupID              *int64
	AuthorAgentID        *int64
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
		query = query.Where("processed_items.keywords ILIKE ? ESCAPE '\\'", ilikeContainsPattern(*params.Keyword))
	}
	if params.Title != nil && *params.Title != "" {
		titlePattern := ilikeContainsPattern(*params.Title)
		query = query.Where("(raw_items.raw_content ILIKE ? ESCAPE '\\' OR processed_items.summary ILIKE ? ESCAPE '\\')",
			titlePattern, titlePattern)
	}
	if len(params.ExcludeEmailSuffixes) > 0 {
		subQuery := db.Table("agents").Select("agent_id")
		conditions := make([]string, 0, len(params.ExcludeEmailSuffixes))
		args := make([]interface{}, 0, len(params.ExcludeEmailSuffixes))
		for _, suffix := range params.ExcludeEmailSuffixes {
			conditions = append(conditions, "agents.email ILIKE ? ESCAPE '\\'")
			args = append(args, ilikeSuffixPattern(suffix))
		}
		subQuery = subQuery.Where(strings.Join(conditions, " OR "), args...)
		query = query.Where("raw_items.author_agent_id NOT IN (?)", subQuery)
	}
	if len(params.IncludeEmailSuffixes) > 0 {
		subQuery := db.Table("agents").Select("agent_id")
		conditions := make([]string, 0, len(params.IncludeEmailSuffixes))
		args := make([]interface{}, 0, len(params.IncludeEmailSuffixes))
		for _, suffix := range params.IncludeEmailSuffixes {
			conditions = append(conditions, "agents.email ILIKE ? ESCAPE '\\'")
			args = append(args, ilikeSuffixPattern(suffix))
		}
		subQuery = subQuery.Where(strings.Join(conditions, " OR "), args...)
		query = query.Where("raw_items.author_agent_id IN (?)", subQuery)
	}
	if params.ItemID != nil {
		query = query.Where("raw_items.item_id = ?", *params.ItemID)
	}
	if params.GroupID != nil {
		query = query.Where("processed_items.group_id = ?", *params.GroupID)
	}
	if params.AuthorAgentID != nil {
		query = query.Where("raw_items.author_agent_id = ?", *params.AuthorAgentID)
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

type UpdateItemParams struct {
	Status *int32
}

func UpdateItem(db *gorm.DB, itemID int64, params UpdateItemParams) (*ItemWithProcessed, error) {
	var count int64
	if err := db.Table("raw_items").Where("item_id = ?", itemID).Count(&count).Error; err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, ErrItemNotFound
	}

	updates := map[string]interface{}{
		"updated_at": time.Now().UnixMilli(),
	}
	if params.Status != nil {
		updates["status"] = int16(*params.Status)
	}

	if err := db.Table("processed_items").Where("item_id = ?", itemID).Updates(updates).Error; err != nil {
		return nil, err
	}

	var item ItemWithProcessed
	err := db.Table("raw_items").
		Select("raw_items.*, processed_items.status, processed_items.summary, processed_items.broadcast_type, processed_items.domains, processed_items.keywords, processed_items.expire_time, processed_items.geo, processed_items.source_type, processed_items.expected_response, processed_items.group_id, processed_items.updated_at").
		Joins("LEFT JOIN processed_items ON raw_items.item_id = processed_items.item_id").
		Where("raw_items.item_id = ?", itemID).
		First(&item).Error
	if err != nil {
		return nil, err
	}
	return &item, nil
}
