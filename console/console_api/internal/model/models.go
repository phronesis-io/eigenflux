package model

// Agent maps to the agents table.
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

// RawItem maps to the raw_items table.
type RawItem struct {
	ItemID        int64  `gorm:"column:item_id;primaryKey"`
	AuthorAgentID int64  `gorm:"column:author_agent_id;not null"`
	RawContent    string `gorm:"column:raw_content;type:text;not null"`
	RawNotes      string `gorm:"column:raw_notes;type:text;default:''"`
	RawURL        string `gorm:"column:raw_url;type:varchar(300);default:''"`
	CreatedAt     int64  `gorm:"column:created_at;not null"`
}

func (RawItem) TableName() string { return "raw_items" }

// MilestoneRule maps to the milestone_rules table.
type MilestoneRule struct {
	RuleID          int64  `gorm:"column:rule_id;primaryKey;autoIncrement"`
	MetricKey       string `gorm:"column:metric_key;type:varchar(64);not null;uniqueIndex:uniq_metric_threshold"`
	Threshold       int64  `gorm:"column:threshold;not null;uniqueIndex:uniq_metric_threshold"`
	RuleEnabled     bool   `gorm:"column:rule_enabled;not null"`
	ContentTemplate string `gorm:"column:content_template;type:text;not null"`
	CreatedAt       int64  `gorm:"column:created_at;not null"`
	UpdatedAt       int64  `gorm:"column:updated_at;not null"`
}

func (MilestoneRule) TableName() string { return "milestone_rules" }

// SystemNotification maps to the system_notifications table.
type SystemNotification struct {
	NotificationID     int64  `gorm:"column:notification_id;primaryKey"`
	Type               string `gorm:"column:type;type:varchar(64);not null"`
	Content            string `gorm:"column:content;type:text;not null"`
	Status             int16  `gorm:"column:status;type:smallint;not null;default:0"`
	AudienceType       string `gorm:"column:audience_type;type:varchar(32);not null;default:broadcast"`
	AudienceExpression string `gorm:"column:audience_expression;type:text;not null;default:''"`
	StartAt            int64  `gorm:"column:start_at;not null;default:0"`
	EndAt              int64  `gorm:"column:end_at;not null;default:0"`
	OfflineAt          int64  `gorm:"column:offline_at;not null;default:0"`
	CreatedAt          int64  `gorm:"column:created_at;not null"`
	UpdatedAt          int64  `gorm:"column:updated_at;not null"`
}

func (SystemNotification) TableName() string { return "system_notifications" }

// BlacklistKeyword maps to the content_blacklist_keywords table.
type BlacklistKeyword struct {
	KeywordID int64  `gorm:"column:keyword_id;primaryKey;autoIncrement"`
	Keyword   string `gorm:"column:keyword;type:text;not null;uniqueIndex:idx_blacklist_keyword_unique"`
	Enabled   bool   `gorm:"column:enabled;not null;default:true"`
	CreatedAt int64  `gorm:"column:created_at;not null"`
	UpdatedAt int64  `gorm:"column:updated_at;not null"`
}

func (BlacklistKeyword) TableName() string { return "content_blacklist_keywords" }

// IsActive returns true if the notification is in the active lifecycle window.
func (n *SystemNotification) IsActive(nowMS int64) bool {
	if n.Status != StatusActive {
		return false
	}
	if n.OfflineAt != 0 {
		return false
	}
	if n.StartAt != 0 && n.StartAt > nowMS {
		return false
	}
	if n.EndAt != 0 && n.EndAt <= nowMS {
		return false
	}
	return true
}

const (
	StatusDraft   int16 = 0
	StatusActive  int16 = 1
	StatusOffline int16 = 2

	AudienceTypeBroadcast  = "broadcast"
	AudienceTypeExpression = "expression"
)
