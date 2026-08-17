package dal

import (
	"context"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestMarkMilestoneEventsNotifiedIsScopedToAgent(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE milestone_events (
		event_id INTEGER PRIMARY KEY, author_agent_id INTEGER NOT NULL,
		notification_status INTEGER NOT NULL, notified_at INTEGER NULL
	)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO milestone_events(event_id,author_agent_id,notification_status)
		VALUES (101,1,0),(202,2,0)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := MarkMilestoneEventsNotified(context.Background(), db, 1, []int64{101, 202}, 999); err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		EventID            int64 `gorm:"column:event_id"`
		NotificationStatus int16 `gorm:"column:notification_status"`
	}
	if err := db.Raw(`SELECT event_id,notification_status FROM milestone_events ORDER BY event_id`).Scan(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].NotificationStatus != 1 || rows[1].NotificationStatus != 0 {
		t.Fatalf("cross-Agent milestone ACK changed another owner's event: %#v", rows)
	}
}
