package dal

import (
	"strings"
	"time"

	"gorm.io/gorm"
)

type Agent struct {
	AgentID            int64  `gorm:"column:agent_id;primaryKey"`
	Email              string `gorm:"column:email;type:varchar(255);not null;unique"`
	AgentName          string `gorm:"column:agent_name;type:varchar(100);not null"`
	Bio                string `gorm:"column:bio;type:text"`
	CreatedAt          int64  `gorm:"column:created_at;not null"`
	UpdatedAt          int64  `gorm:"column:updated_at;not null"`
	ProfileCompletedAt *int64 `gorm:"column:profile_completed_at"`
}

func (Agent) TableName() string { return "agents" }

type AgentProfile struct {
	AgentID          int64  `gorm:"column:agent_id;primaryKey"`
	Status           int16  `gorm:"column:status;type:smallint;not null;default:0"`
	Keywords         string `gorm:"column:keywords;type:text"`
	Country          string `gorm:"column:country;type:varchar(100);default:''"`
	ProfileEmbedding []byte `gorm:"column:profile_embedding;type:bytea"`
	EmbeddingModel   string `gorm:"column:embedding_model;type:varchar(100);default:''"`
	UpdatedAt        int64  `gorm:"column:updated_at;not null"`
}

func (AgentProfile) TableName() string { return "agent_profiles" }

func CreateAgent(db *gorm.DB, agent *Agent) error {
	now := time.Now().UnixMilli()
	agent.CreatedAt = now
	agent.UpdatedAt = now
	return db.Create(agent).Error
}

func GetAgentByID(db *gorm.DB, agentID int64) (*Agent, error) {
	var agent Agent
	err := db.Where("agent_id = ?", agentID).First(&agent).Error
	return &agent, err
}

func GetAgentByEmail(db *gorm.DB, email string) (*Agent, error) {
	var agent Agent
	err := db.Where("email = ?", email).First(&agent).Error
	return &agent, err
}

func UpdateAgentFields(db *gorm.DB, agentID int64, updates map[string]interface{}) error {
	updates["updated_at"] = time.Now().UnixMilli()
	return db.Model(&Agent{}).Where("agent_id = ?", agentID).Updates(updates).Error
}

func GetAgentProfile(db *gorm.DB, agentID int64) (*AgentProfile, error) {
	var profile AgentProfile
	err := db.Where("agent_id = ?", agentID).First(&profile).Error
	return &profile, err
}

func CreateAgentProfile(db *gorm.DB, profile *AgentProfile) error {
	profile.UpdatedAt = time.Now().UnixMilli()
	return db.Create(profile).Error
}

func UpdateAgentProfileStatus(db *gorm.DB, agentID int64, status int16) error {
	return db.Model(&AgentProfile{}).Where("agent_id = ?", agentID).Updates(map[string]interface{}{
		"status":     status,
		"updated_at": time.Now().UnixMilli(),
	}).Error
}

func UpdateAgentProfileKeywords(db *gorm.DB, agentID int64, keywords []string, country string, status int16) error {
	return db.Model(&AgentProfile{}).Where("agent_id = ?", agentID).Updates(map[string]interface{}{
		"keywords":   strings.Join(keywords, ","),
		"country":    country,
		"status":     status,
		"updated_at": time.Now().UnixMilli(),
	}).Error
}

// MatchAgentsByKeywords finds agents whose profile keywords match any of the given keywords
func MatchAgentsByKeywords(db *gorm.DB, keywords []string, excludeAgentID *int64, limit int) ([]int64, error) {
	if len(keywords) == 0 {
		return []int64{}, nil
	}

	if limit <= 0 {
		limit = 100
	}

	query := db.Model(&AgentProfile{}).Select("agent_id")

	// Build OR conditions for ILIKE matching
	var conditions []string
	var args []interface{}
	for _, keyword := range keywords {
		conditions = append(conditions, "keywords ILIKE ?")
		args = append(args, "%"+keyword+"%")
	}

	query = query.Where(strings.Join(conditions, " OR "), args...)

	// Exclude specified agent if provided
	if excludeAgentID != nil {
		query = query.Where("agent_id != ?", *excludeAgentID)
	}

	// Only return agents with completed profiles (status = 3)
	query = query.Where("status = ?", 3)

	query = query.Limit(limit)

	var agentIDs []int64
	err := query.Pluck("agent_id", &agentIDs).Error
	if err != nil {
		return nil, err
	}

	return agentIDs, nil
}

func UpdateAgentProfileEmbedding(db *gorm.DB, agentID int64, embedding []byte, model string) error {
	return db.Model(&AgentProfile{}).Where("agent_id = ?", agentID).Updates(map[string]interface{}{
		"profile_embedding": embedding,
		"embedding_model":   model,
		"updated_at":        time.Now().UnixMilli(),
	}).Error
}
