package agti

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"
)

// Session maps to agti_sessions. JSONB columns are stored as raw JSON strings
// (same pattern as api/dal ActivityLog.Detail).
type Session struct {
	SessionID string `gorm:"column:session_id;primaryKey"`
	QuestionIDs string `gorm:"column:question_ids;type:jsonb;not null"`
	// Nullable JSONB columns must be pointers: an empty string is not valid
	// JSON, so gorm writing "" would fail with SQLSTATE 22P02.
	AgentAnswers  *string `gorm:"column:agent_answers;type:jsonb"`
	AgentLockedAt int64   `gorm:"column:agent_locked_at;not null;default:0"`
	HumanAnswers  *string `gorm:"column:human_answers;type:jsonb"`
	ResultID      string  `gorm:"column:result_id;not null;default:''"`
	ClientIP      string  `gorm:"column:client_ip;not null;default:''"`
	CreatedAt     int64   `gorm:"column:created_at;not null"`
}

func (Session) TableName() string { return "agti_sessions" }

// Result maps to agti_results. Payload is the fully rendered result page data;
// rows are immutable so shared links never change.
type Result struct {
	ResultID   string `gorm:"column:result_id;primaryKey"`
	SessionID  string `gorm:"column:session_id;not null"`
	TypeCode   string `gorm:"column:type_code;not null"`
	MatchCount int    `gorm:"column:match_count;not null"`
	Payload    string `gorm:"column:payload;type:jsonb;not null"`
	CreatedAt  int64  `gorm:"column:created_at;not null"`
}

func (Result) TableName() string { return "agti_results" }

// ErrLocked is returned when the agent tries to submit twice (commit-reveal).
var ErrLocked = errors.New("agent answers already locked")

// ErrNotLocked is returned when the human submits before the agent locked.
var ErrNotLocked = errors.New("agent has not submitted yet")

// NewID returns a 16-char hex ID (same shape as the demo's session/result ids).
func NewID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failure is unrecoverable
	}
	return hex.EncodeToString(b)
}

// CreateSession stores a new session with its picked question IDs.
func CreateSession(db *gorm.DB, sessionID string, questionIDs []string, clientIP string) error {
	ids, _ := json.Marshal(questionIDs)
	return db.Create(&Session{
		SessionID:   sessionID,
		QuestionIDs: string(ids),
		ClientIP:    clientIP,
		CreatedAt:   time.Now().UnixMilli(),
	}).Error
}

// GetSession loads a session, or (nil, nil) when absent.
func GetSession(db *gorm.DB, sessionID string) (*Session, error) {
	var s Session
	err := db.Where("session_id = ?", sessionID).First(&s).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// QuestionIDList decodes the session's stored question IDs.
func (s *Session) QuestionIDList() []string {
	var ids []string
	_ = json.Unmarshal([]byte(s.QuestionIDs), &ids)
	return ids
}

// AgentAnswerMap decodes the locked agent answers (empty map if not locked).
func (s *Session) AgentAnswerMap() map[string]string {
	out := map[string]string{}
	if s.AgentAnswers != nil {
		_ = json.Unmarshal([]byte(*s.AgentAnswers), &out)
	}
	return out
}

// LockAgentAnswers writes the agent's answers exactly once (commit-reveal).
// The conditional UPDATE is the lock: a second submit matches zero rows.
func LockAgentAnswers(db *gorm.DB, sessionID string, answers map[string]string) error {
	data, _ := json.Marshal(answers)
	res := db.Model(&Session{}).
		Where("session_id = ? AND agent_locked_at = 0", sessionID).
		Updates(map[string]interface{}{
			"agent_answers":   string(data),
			"agent_locked_at": time.Now().UnixMilli(),
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrLocked
	}
	return nil
}

// SubmitHuman stores the human's answers and the computed result in one
// transaction. Idempotent: a second submit returns the existing result ID
// without recomputing, so refreshing the page can't change the outcome.
func SubmitHuman(db *gorm.DB, sessionID string, humanAnswers map[string]string, build func(s *Session) (*Result, error)) (string, error) {
	var resultID string
	err := db.Transaction(func(tx *gorm.DB) error {
		var s Session
		if err := tx.Where("session_id = ?", sessionID).First(&s).Error; err != nil {
			return err
		}
		if s.AgentLockedAt == 0 {
			return ErrNotLocked
		}
		if s.ResultID != "" {
			resultID = s.ResultID
			return nil
		}
		r, err := build(&s)
		if err != nil {
			return err
		}
		if err := tx.Create(r).Error; err != nil {
			return err
		}
		data, _ := json.Marshal(humanAnswers)
		if err := tx.Model(&Session{}).Where("session_id = ?", sessionID).
			Updates(map[string]interface{}{
				"human_answers": string(data),
				"result_id":     r.ResultID,
			}).Error; err != nil {
			return err
		}
		resultID = r.ResultID
		return nil
	})
	return resultID, err
}

// GetResult loads a result, or (nil, nil) when absent.
func GetResult(db *gorm.DB, resultID string) (*Result, error) {
	var r Result
	err := db.Where("result_id = ?", resultID).First(&r).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// CleanupExpired deletes unfinished sessions older than ttl. Completed
// sessions and results are kept — they back shared links and funnel stats.
func CleanupExpired(db *gorm.DB, ttl time.Duration) (int64, error) {
	cutoff := time.Now().Add(-ttl).UnixMilli()
	res := db.Where("result_id = '' AND created_at < ?", cutoff).Delete(&Session{})
	return res.RowsAffected, res.Error
}
