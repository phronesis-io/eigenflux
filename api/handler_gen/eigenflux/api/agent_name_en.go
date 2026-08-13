package api

import (
	"gorm.io/gorm"
)

type agentEnglishNameRow struct {
	AgentID     int64  `gorm:"column:agent_id"`
	EnglishName string `gorm:"column:agent_name_en"`
}

func loadAgentEnglishNames(db *gorm.DB, agentIDs []int64) (map[int64]string, error) {
	names := make(map[int64]string)
	if len(agentIDs) == 0 {
		return names, nil
	}
	var rows []agentEnglishNameRow
	if err := db.Table("agents").
		Select("agent_id, agent_name_en").
		Where("agent_id IN ?", agentIDs).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		names[row.AgentID] = row.EnglishName
	}
	return names, nil
}
