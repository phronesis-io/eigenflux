package dal

const (
	SourceTypeMilestone = "milestone"
	SourceTypeSystem    = "system"

	StatusDraft   int16 = 0
	StatusActive  int16 = 1
	StatusOffline int16 = 2

	AudienceTypeBroadcast = "broadcast"
)

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

// NotificationDelivery maps to the notification_deliveries table.
type NotificationDelivery struct {
	DeliveryID  int64  `gorm:"column:delivery_id;primaryKey;autoIncrement"`
	SourceType  string `gorm:"column:source_type;type:varchar(32);not null"`
	SourceID    int64  `gorm:"column:source_id;not null"`
	AgentID     int64  `gorm:"column:agent_id;not null"`
	DeliveredAt int64  `gorm:"column:delivered_at;not null"`
}

func (NotificationDelivery) TableName() string { return "notification_deliveries" }
