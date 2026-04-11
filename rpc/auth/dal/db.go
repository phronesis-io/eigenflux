package dal

import (
	"time"

	"gorm.io/gorm"
)

// AuthEmailChallenge maps to auth_email_challenges table.
type AuthEmailChallenge struct {
	ChallengeID     string  `gorm:"column:challenge_id;primaryKey"`
	LoginMethod     string  `gorm:"column:login_method;not null"`
	Email           *string `gorm:"column:email"`
	CodeHash        string  `gorm:"column:code_hash;not null"`
	VerifyTokenHash *string `gorm:"column:verify_token_hash"`
	MockVerifyToken *string `gorm:"column:mock_verify_token"`
	Status          int16   `gorm:"column:status;not null;default:0"`
	AttemptCount    int     `gorm:"column:attempt_count;not null;default:0"`
	MaxAttempts     int     `gorm:"column:max_attempts;not null;default:5"`
	ExpireAt        int64   `gorm:"column:expire_at;not null"`
	CreatedAt       int64   `gorm:"column:created_at;not null"`
	ConsumedAt      *int64  `gorm:"column:consumed_at"`
	ClientIP        *string `gorm:"column:client_ip"`
	UserAgent       *string `gorm:"column:user_agent"`
}

func (AuthEmailChallenge) TableName() string { return "auth_email_challenges" }

// AgentSession maps to agent_sessions table.
type AgentSession struct {
	SessionID  int64   `gorm:"column:session_id;primaryKey;autoIncrement"`
	AgentID    int64   `gorm:"column:agent_id;not null"`
	TokenHash  string  `gorm:"column:token_hash;not null;unique"`
	Status     int16   `gorm:"column:status;not null;default:0"`
	ExpireAt   int64   `gorm:"column:expire_at;not null"`
	CreatedAt  int64   `gorm:"column:created_at;not null"`
	LastSeenAt int64   `gorm:"column:last_seen_at;not null"`
	ClientIP   *string `gorm:"column:client_ip"`
	UserAgent  *string `gorm:"column:user_agent"`
}

func (AgentSession) TableName() string { return "agent_sessions" }

// Agent is a minimal view of the agents table for auth purposes.
type Agent struct {
	AgentID            int64  `gorm:"column:agent_id;primaryKey"`
	Email              string `gorm:"column:email;not null;unique"`
	AgentName          string `gorm:"column:agent_name;not null"`
	Bio                string `gorm:"column:bio"`
	CreatedAt          int64  `gorm:"column:created_at;not null"`
	UpdatedAt          int64  `gorm:"column:updated_at;not null"`
	EmailVerifiedAt    *int64 `gorm:"column:email_verified_at"`
	ProfileCompletedAt *int64 `gorm:"column:profile_completed_at"`
}

func (Agent) TableName() string { return "agents" }

// CreateChallenge inserts a new auth_email_challenges row.
func CreateChallenge(db *gorm.DB, challenge *AuthEmailChallenge) error {
	return db.Create(challenge).Error
}

// GetChallenge fetches a challenge by challenge_id.
func GetChallenge(db *gorm.DB, challengeID string) (*AuthEmailChallenge, error) {
	var c AuthEmailChallenge
	err := db.Where("challenge_id = ?", challengeID).First(&c).Error
	return &c, err
}

// IncrementChallengeAttempts increments the attempt_count by 1.
func IncrementChallengeAttempts(db *gorm.DB, challengeID string) error {
	return db.Model(&AuthEmailChallenge{}).
		Where("challenge_id = ?", challengeID).
		UpdateColumn("attempt_count", gorm.Expr("attempt_count + 1")).Error
}

// ConsumeChallenge atomically marks a pending challenge as consumed.
// Returns true if exactly one row was consumed, false if the challenge is no longer consumable.
func ConsumeChallenge(db *gorm.DB, challengeID string, now int64) (bool, error) {
	tx := db.Model(&AuthEmailChallenge{}).
		Where("challenge_id = ? AND status = 0 AND expire_at >= ? AND attempt_count < max_attempts", challengeID, now).
		Updates(map[string]interface{}{
			"status":      1,
			"consumed_at": now,
		})
	if tx.Error != nil {
		return false, tx.Error
	}
	return tx.RowsAffected == 1, nil
}

// RevokeChallenge marks a challenge as revoked (status=3).
func RevokeChallenge(db *gorm.DB, challengeID string) error {
	return db.Model(&AuthEmailChallenge{}).
		Where("challenge_id = ?", challengeID).
		UpdateColumn("status", 3).Error
}

// CreateSession inserts a new agent_sessions row.
func CreateSession(db *gorm.DB, session *AgentSession) error {
	now := time.Now().UnixMilli()
	session.CreatedAt = now
	session.LastSeenAt = now
	return db.Create(session).Error
}

// GetSessionByTokenHash fetches an active session by token_hash.
func GetSessionByTokenHash(db *gorm.DB, tokenHash string) (*AgentSession, error) {
	var s AgentSession
	now := time.Now().UnixMilli()
	err := db.Where("token_hash = ? AND status = 0 AND expire_at > ?", tokenHash, now).First(&s).Error
	return &s, err
}

// GetAgentByEmail looks up an agent by email, returns nil if not found.
func GetAgentByEmail(db *gorm.DB, email string) (*Agent, error) {
	var agent Agent
	err := db.Where("email = ?", email).First(&agent).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &agent, nil
}

// AgentProfile is a minimal view of agent_profiles for creating the initial record.
type AgentProfile struct {
	AgentID   int64  `gorm:"column:agent_id;primaryKey"`
	Status    int16  `gorm:"column:status;not null;default:0"`
	Keywords  string `gorm:"column:keywords;type:text"`
	UpdatedAt int64  `gorm:"column:updated_at;not null"`
}

func (AgentProfile) TableName() string { return "agent_profiles" }

// CreateMinimalAgent creates a new agent with only email set and empty profile fields.
// Also creates the initial agent_profiles record (status=0).
func CreateMinimalAgent(db *gorm.DB, agentID int64, email string) (*Agent, error) {
	now := time.Now().UnixMilli()
	agent := &Agent{
		AgentID:   agentID,
		Email:     email,
		AgentName: "",
		Bio:       "",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := db.Create(agent).Error; err != nil {
		return nil, err
	}
	// Create initial agent_profiles record
	profile := &AgentProfile{
		AgentID:   agent.AgentID,
		Status:    0,
		UpdatedAt: now,
	}
	db.Create(profile) // best-effort
	return agent, nil
}

// SetEmailVerifiedAt sets email_verified_at if it is currently NULL.
func SetEmailVerifiedAt(db *gorm.DB, agentID int64, now int64) error {
	return db.Model(&Agent{}).
		Where("agent_id = ? AND email_verified_at IS NULL", agentID).
		UpdateColumn("email_verified_at", now).Error
}

// UpdateSessionActivity updates last_seen_at and extends expire_at for a session.
func UpdateSessionActivity(db *gorm.DB, sessionID int64, now int64, expireAt int64) error {
	return db.Model(&AgentSession{}).
		Where("session_id = ?", sessionID).
		Updates(map[string]interface{}{
			"last_seen_at": now,
			"expire_at":    expireAt,
		}).Error
}
