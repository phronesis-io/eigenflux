package pm

import (
	"fmt"
	"reflect"
	"strconv"
	"testing"
	"time"

	"eigenflux_server/tests/testutil"
)

func TestListFriends_OfficialPagesRespectLimit(t *testing.T) {
	testutil.WaitForAPI(t)
	emails := []string{"official_page_owner@test.com", "official_page_old@test.com", "official_page_new@test.com", "official_page_normal_old@test.com", "official_page_normal_new@test.com"}
	testutil.CleanupTestEmails(t, emails...)
	t.Cleanup(func() { testutil.CleanupTestEmails(t, emails...) })
	var ids []int64
	var owner map[string]interface{}
	for i, email := range emails {
		account := testutil.RegisterAgent(t, email, fmt.Sprintf("Pagination %d", i), "pagination regression")
		id, err := strconv.ParseInt(account["agent_id"].(string), 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		if i == 0 {
			owner = account
		}
	}
	defer cleanRelationsData(t, ids...)
	// Create both official relations before the ordinary ones: their older IDs
	// must not exclude newer ordinary friends when an official page ends.
	for _, id := range ids[1:3] {
		if _, err := testutil.TestDB.Exec("UPDATE agents SET is_official = true WHERE agent_id = $1", id); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range ids[1:] {
		if _, err := testutil.TestDB.Exec("INSERT INTO user_relations (from_uid,to_uid,rel_type,created_at,remark) VALUES ($1,$2,1,$3,'pagination remark')", ids[0], id, time.Now().UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{strconv.FormatInt(ids[2], 10), strconv.FormatInt(ids[1], 10), strconv.FormatInt(ids[4], 10), strconv.FormatInt(ids[3], 10)}
	for _, limit := range []int{1, 2, 3} {
		t.Run(fmt.Sprintf("limit_%d", limit), func(t *testing.T) {
			cursor := "0"
			var got []string
			for page := 0; page < 6; page++ {
				response := testutil.DoGet(t, fmt.Sprintf("/api/v1/relations/friends?limit=%d&cursor=%s", limit, cursor), owner["token"].(string))
				if response["code"] != float64(0) {
					t.Fatalf("list failed: %v", response)
				}
				data := response["data"].(map[string]interface{})
				if data["total"] != float64(4) {
					t.Fatalf("total=%v, want 4", data["total"])
				}
				friends, _ := data["friends"].([]interface{})
				if len(friends) > limit {
					t.Fatalf("returned %d friends, limit %d", len(friends), limit)
				}
				if len(friends) == 0 {
					break
				}
				for _, entry := range friends {
					friend := entry.(map[string]interface{})
					got = append(got, friend["agent_id"].(string))
					if friend["remark"] != "pagination remark" {
						t.Fatalf("lost remark: %v", friend)
					}
				}
				next, _ := data["next_cursor"].(string)
				if next == "" || next == "0" || next == cursor {
					t.Fatalf("invalid next cursor: %v", data)
				}
				cursor = next
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("paged friends=%v, want %v", got, want)
			}
		})
	}
}
