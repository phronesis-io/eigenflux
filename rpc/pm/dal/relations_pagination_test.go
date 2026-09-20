package dal

import (
	"fmt"
	"os"
	"reflect"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestPostgresListFriendsOfficialPagination(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN is required for the PostgreSQL friend-pagination contract")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	// Connection-local tables exercise PostgreSQL ordering without modifying
	// any shared integration accounts or their relationships.
	for _, statement := range []string{
		`CREATE TEMP TABLE agents (agent_id BIGINT PRIMARY KEY, agent_name TEXT, bio TEXT, is_official BOOLEAN)`,
		`CREATE TEMP TABLE user_relations (id BIGINT PRIMARY KEY, from_uid BIGINT, to_uid BIGINT, rel_type SMALLINT, created_at BIGINT, remark TEXT)`,
		`INSERT INTO agents VALUES (11,'official old','bio',true),(12,'official new','bio',true),(13,'normal old','bio',false),(14,'normal new','bio',false),(15,'nullable normal','bio',null)`,
		`INSERT INTO user_relations VALUES
          (10,1,11,1,1000,'old official'), (30,1,12,1,3000,'new official'),
          (20,1,13,1,2000,'old normal'), (50,1,14,1,5000,'new normal'),
          (40,1,15,1,4000,'nullable'), (60,2,11,1,6000,'other owner'),
          (70,1,11,2,7000,'block')`,
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, limit := range []int{1, 2, 3, 5, 10} {
		t.Run(fmt.Sprintf("limit_%d", limit), func(t *testing.T) {
			var got []int64
			var cursor int64
			for page := 0; page < 7; page++ {
				friends, err := ListFriends(db, 1, cursor, limit)
				if err != nil {
					t.Fatal(err)
				}
				if len(friends) > limit {
					t.Fatalf("page returned %d friends, limit %d", len(friends), limit)
				}
				total, err := CountFriends(db, 1)
				if err != nil || total != 5 {
					t.Fatalf("total=%d err=%v, want 5", total, err)
				}
				if len(friends) == 0 {
					break
				}
				for _, friend := range friends {
					got = append(got, friend.RelationID)
					if friend.AgentName == "" || friend.Bio != "bio" || friend.Remark == "" || friend.FriendSince != friend.RelationID*100 {
						t.Fatalf("lost friend metadata: %+v", friend)
					}
				}
				cursor = friends[len(friends)-1].RelationID
			}
			if want := []int64{30, 10, 50, 40, 20}; !reflect.DeepEqual(got, want) {
				t.Fatalf("paged relation IDs=%v, want %v", got, want)
			}
		})
	}
	// Foreign and non-friend anchors cannot restart the official group.
	for _, cursor := range []int64{60, 70, 99} {
		friends, err := ListFriends(db, 1, cursor, 10)
		if err != nil {
			t.Fatal(err)
		}
		var got []int64
		for _, friend := range friends {
			got = append(got, friend.RelationID)
		}
		if want := []int64{50, 40, 20}; !reflect.DeepEqual(got, want) {
			t.Fatalf("out-of-scope cursor %d returned %v, want %v", cursor, got, want)
		}
	}
	if err := db.Exec(`UPDATE agents SET is_official = false`).Error; err != nil {
		t.Fatal(err)
	}
	friends, err := ListFriends(db, 1, 40, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(friends) != 2 || friends[0].RelationID != 30 || friends[1].RelationID != 20 {
		t.Fatalf("ordinary cursor order changed: %+v", friends)
	}
}
