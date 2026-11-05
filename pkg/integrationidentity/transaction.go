package integrationidentity

import (
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// LockManagedAgent serializes managed business mutations in application-first
// order and checks current global, application and Agent control. Installation
// and runtime ownership remain the HTTP boundary's responsibility.
func LockManagedAgent(tx *gorm.DB, agentID int64) error {
	var owner struct{ AppID string }
	if err := tx.Table("integration_agents").Select("app_id").Where("agent_id = ?", agentID).First(&owner).Error; err != nil {
		return err
	}
	var app App
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&app, "app_id = ?", owner.AppID).Error; err != nil {
		return err
	}
	var agent Agent
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&agent, "agent_id = ?", agentID).Error; err != nil {
		return err
	}
	var global struct{ Enabled bool }
	if err := tx.Table("integration_control").Where("id = 1").First(&global).Error; err != nil {
		return err
	}
	if !global.Enabled || !app.Enabled || !agent.Enabled {
		return deny("INTEGRATION_DISABLED")
	}
	return nil
}
