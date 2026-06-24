package install

// Token maps to install_tokens. One row per minted invite token. status flips
// pending->installed exactly once on the first /report (the conversion);
// report_count counts every raw report hit (including replays). All UTM and
// referrer data is stored here and recovered by token on report — the token
// itself is an opaque key with no embedded attribution.
type Token struct {
	Token       string `gorm:"column:token;primaryKey"`
	UTMSource   string `gorm:"column:utm_source;not null;default:''"`
	UTMMedium   string `gorm:"column:utm_medium;not null;default:''"`
	UTMCampaign string `gorm:"column:utm_campaign;not null;default:''"`
	UTMContent  string `gorm:"column:utm_content;not null;default:''"`
	UTMTerm     string `gorm:"column:utm_term;not null;default:''"`
	Channel     string `gorm:"column:channel;not null;default:''"`
	Referrer    string `gorm:"column:referrer;type:text;not null;default:''"`
	Status      string `gorm:"column:status;not null;default:'pending'"`
	ReportCount int    `gorm:"column:report_count;not null;default:0"`
	ClientIP    string `gorm:"column:client_ip;not null;default:''"`
	CreatedAt   int64  `gorm:"column:created_at;not null"`
	ReportedAt  int64  `gorm:"column:reported_at;not null;default:0"`
}

func (Token) TableName() string { return "install_tokens" }

const (
	StatusPending   = "pending"
	StatusInstalled = "installed"
)
