package consumer

import (
	"eigenflux_server/pkg/config"
	"gorm.io/gorm"
)

func pgcSummaryPublisher(db *gorm.DB, authorID int64, suffixes []string) (bool, error) {
	var row struct{ Email string }
	if err := db.Table("agents").Select("email").Where("agent_id = ?", authorID).Take(&row).Error; err != nil {
		return false, err
	}
	return config.EmailMatchesAnySuffix(row.Email, suffixes), nil
}
