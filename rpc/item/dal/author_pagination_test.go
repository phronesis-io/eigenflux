package dal

import (
	"reflect"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestGetItemStatsByAuthorPagination(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { sqlDB.Close() })
	if err := db.AutoMigrate(&ItemStats{}); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"CREATE TABLE processed_items (item_id BIGINT PRIMARY KEY, status INTEGER, summary TEXT, broadcast_type TEXT)",
		"CREATE TABLE raw_items (item_id BIGINT PRIMARY KEY, raw_content TEXT)",
		"CREATE TABLE conversations (origin_id BIGINT, origin_type TEXT)",
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	// ID order deliberately differs from helpful-count order; ties span pages.
	for _, row := range []ItemStats{
		{ItemID: 60, AuthorAgentID: 1, Score1Count: 1, CreatedAt: 100},
		{ItemID: 50, AuthorAgentID: 1, Score1Count: 5, CreatedAt: 100},
		{ItemID: 40, AuthorAgentID: 1, Score2Count: 3, CreatedAt: 100},
		{ItemID: 30, AuthorAgentID: 1, Score1Count: 2, Score2Count: 3, CreatedAt: 100},
		{ItemID: 20, AuthorAgentID: 1, Score1Count: 10, CreatedAt: 100},
		{ItemID: 10, AuthorAgentID: 1, Score1Count: 3, CreatedAt: 100},
		{ItemID: 70, AuthorAgentID: 1, Score1Count: 20, CreatedAt: 50},
		{ItemID: 80, AuthorAgentID: 2, Score1Count: 30, CreatedAt: 100},
	} {
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Exec("INSERT INTO processed_items VALUES (?, 3, 'summary', 'info')", row.ItemID).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Exec("INSERT INTO raw_items VALUES (?, 'content')", row.ItemID).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name, filter string
		want         []int64
	}{
		{"hottest", "hottest", []int64{20, 50, 30, 40, 10, 60}},
		{"latest", "", []int64{60, 50, 40, 30, 20, 10}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []int64
			var cursor int64
			for page := 0; page < 5; page++ {
				rows, err := GetItemStatsByAuthor(db, 1, cursor, 2, 100, tc.filter)
				if err != nil {
					t.Fatal(err)
				}
				if len(rows) == 0 {
					break
				}
				for _, row := range rows {
					got = append(got, row.ItemID)
				}
				cursor = rows[len(rows)-1].ItemID
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("paginated IDs = %v, want %v", got, tc.want)
			}
		})
	}
	for _, cursor := range []int64{80, 999} {
		rows, err := GetItemStatsByAuthor(db, 1, cursor, 2, 100, "hottest")
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 0 {
			t.Fatalf("cursor %d outside author items returned %d rows", cursor, len(rows))
		}
	}
}
