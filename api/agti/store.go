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
	SessionID   string `gorm:"column:session_id;primaryKey"`
	QuestionIDs string `gorm:"column:question_ids;type:jsonb;not null"`
	// Nullable JSONB columns must be pointers: an empty string is not valid
	// JSON, so gorm writing "" would fail with SQLSTATE 22P02.
	AgentAnswers  *string `gorm:"column:agent_answers;type:jsonb"`
	AgentLockedAt int64   `gorm:"column:agent_locked_at;not null;default:0"`
	HumanAnswers  *string `gorm:"column:human_answers;type:jsonb"`
	ResultID      string  `gorm:"column:result_id;not null;default:''"`
	ClientIP      string  `gorm:"column:client_ip;not null;default:''"`
	CreatedAt     int64   `gorm:"column:created_at;not null"`
	// Agent self-reported identity + q0 ("how do you address your master").
	AgentName     string `gorm:"column:agent_name;not null;default:''"`
	ModelName     string `gorm:"column:model_name;not null;default:''"`
	MasterAddress string `gorm:"column:master_address;not null;default:''"` // agent: 怎么称呼主人
	HumanName     string `gorm:"column:human_name;not null;default:''"`     // human: 真名/想被怎么称呼
	Ref           string `gorm:"column:ref;not null;default:''"`            // KOL 追踪码（01..50），随提示词带入
}

func (Session) TableName() string { return "agti_sessions" }

// TrackEvent maps to agti_track_events: one funnel event line, tagged with the
// KOL ref so the whole funnel can be attributed per KOL.
type TrackEvent struct {
	ID        int64  `gorm:"column:id;primaryKey"`
	Ref       string `gorm:"column:ref;not null;default:''"`
	Event     string `gorm:"column:event;not null"`
	SessionID string `gorm:"column:session_id;not null;default:''"`
	ClientIP  string `gorm:"column:client_ip;not null;default:''"`
	CreatedAt int64  `gorm:"column:created_at;not null"`
}

func (TrackEvent) TableName() string { return "agti_track_events" }

// Referral maps to agti_referrals: KOL code -> optional label.
type Referral struct {
	Code      string `gorm:"column:code;primaryKey"`
	Label     string `gorm:"column:label;not null;default:''"`
	CreatedAt int64  `gorm:"column:created_at;not null;default:0"`
}

func (Referral) TableName() string { return "agti_referrals" }

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

// CreateSession stores a new session with its picked question IDs and the KOL
// ref (may be "") that drove it.
func CreateSession(db *gorm.DB, sessionID string, questionIDs []string, clientIP, ref string) error {
	ids, _ := json.Marshal(questionIDs)
	return db.Create(&Session{
		SessionID:   sessionID,
		QuestionIDs: string(ids),
		ClientIP:    clientIP,
		Ref:         ref,
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

// LockAgentAnswers writes the agent's answers + self-reported identity exactly
// once (commit-reveal). The conditional UPDATE is the lock: a second submit
// matches zero rows. agentName/modelName/masterAddr are free text (may be "").
func LockAgentAnswers(db *gorm.DB, sessionID string, answers map[string]string, agentName, modelName, masterAddr string) error {
	data, _ := json.Marshal(answers)
	res := db.Model(&Session{}).
		Where("session_id = ? AND agent_locked_at = 0", sessionID).
		Updates(map[string]interface{}{
			"agent_answers":   string(data),
			"agent_locked_at": time.Now().UnixMilli(),
			"agent_name":      agentName,
			"model_name":      modelName,
			"master_address":  masterAddr,
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
func SubmitHuman(db *gorm.DB, sessionID string, humanAnswers map[string]string, humanName string, build func(s *Session) (*Result, error)) (string, error) {
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
				"human_name":    humanName,
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

// LogEvent appends one funnel event (best-effort: tracking must never break the
// user flow, so callers ignore the error).
func LogEvent(db *gorm.DB, ref, ev, sessionID, clientIP string) error {
	return db.Create(&TrackEvent{
		Ref:       ref,
		Event:     ev,
		SessionID: sessionID,
		ClientIP:  clientIP,
		CreatedAt: time.Now().UnixMilli(),
	}).Error
}

// RecentRefByIP returns the most recent skills_view/join_view ref from the same
// IP within window — a fallback attribution for agents that drop the ?ref= when
// calling quiz/new. Returns "" when there's no recent match.
func RecentRefByIP(db *gorm.DB, clientIP string, window time.Duration) string {
	if clientIP == "" {
		return ""
	}
	cutoff := time.Now().Add(-window).UnixMilli()
	var e TrackEvent
	err := db.Where("client_ip = ? AND ref <> '' AND event IN ('skills_view','join_view') AND created_at >= ?", clientIP, cutoff).
		Order("created_at DESC").First(&e).Error
	if err != nil {
		return ""
	}
	return e.Ref
}

// FunnelRow is one KOL's funnel counts. View events count raw hits; session
// events count distinct sessions (so retries/refreshes don't inflate).
type FunnelRow struct {
	Ref           string `json:"ref"`
	Label         string `json:"label"`
	SkillsView    int64  `json:"skills_view"`    // Agent 看了 skills（原始次数）
	QuizStart     int64  `json:"quiz_start"`     // Agent 起了测验（去重 session）
	AgentLock     int64  `json:"agent_lock"`     // Agent 锁定答案
	HumanOpen     int64  `json:"human_open"`     // 用户打开答题页
	HumanComplete int64  `json:"human_complete"` // 用户答完
	JoinView      int64  `json:"join_view"`      // 发起加入 EigenFlux（原始次数）
}

// FunnelStats returns one row per registered KOL code (plus any code seen in
// events but not registered), with counts per funnel step.
func FunnelStats(db *gorm.DB) ([]FunnelRow, error) {
	// Aggregate events: raw count + distinct-session count, per (ref, event).
	type agg struct {
		Ref   string
		Event string
		Total int64
		Uniq  int64
	}
	var rows []agg
	if err := db.Model(&TrackEvent{}).
		Select("ref, event, COUNT(*) AS total, COUNT(DISTINCT session_id) AS uniq").
		Group("ref, event").Scan(&rows).Error; err != nil {
		return nil, err
	}

	out := map[string]*FunnelRow{}
	get := func(ref string) *FunnelRow {
		if out[ref] == nil {
			out[ref] = &FunnelRow{Ref: ref}
		}
		return out[ref]
	}
	for _, r := range rows {
		if r.Ref == "" {
			continue // untracked (organic) traffic — not a KOL row
		}
		row := get(r.Ref)
		switch r.Event {
		case "skills_view":
			row.SkillsView = r.Total
		case "quiz_start":
			row.QuizStart = r.Uniq
		case "agent_lock":
			row.AgentLock = r.Uniq
		case "human_open":
			row.HumanOpen = r.Uniq
		case "human_complete":
			row.HumanComplete = r.Uniq
		case "join_view":
			row.JoinView = r.Total
		}
	}

	// Merge in registered referrals (labels + zero-traffic codes).
	var refs []Referral
	if err := db.Find(&refs).Error; err != nil {
		return nil, err
	}
	for _, ref := range refs {
		get(ref.Code).Label = ref.Label
	}

	list := make([]FunnelRow, 0, len(out))
	for _, r := range out {
		list = append(list, *r)
	}
	return list, nil
}
