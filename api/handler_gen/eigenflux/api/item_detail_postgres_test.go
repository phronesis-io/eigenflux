package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/route/param"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"eigenflux_server/pkg/agentidentity"
	"eigenflux_server/pkg/db"
)

func TestPostgresBroadcastDetailOwnerAndGuest(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN is required for broadcast detail contracts")
	}
	database, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := database.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })
	tx := database.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	previous := db.DB
	db.DB = tx
	t.Cleanup(func() { db.DB = previous })

	base, created := time.Now().UnixNano(), time.Now().UnixMilli()-10000
	owner, guest, helpful := base, base+1, base+2
	itemID := base + 100
	exec := func(query string, args ...interface{}) {
		t.Helper()
		if err := tx.Exec(query, args...).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []int64{owner, guest, helpful} {
		shortID, err := agentidentity.GenerateShortID()
		if err != nil {
			t.Fatal(err)
		}
		exec(`INSERT INTO agents (agent_id, short_id, email, agent_name, agent_name_en, bio, created_at, updated_at)
			VALUES (?, ?, ?, '原始名字', 'Original Name', '', ?, ?)`, id, shortID,
			fmt.Sprintf("broadcast-detail-%d@example.test", id), created, created)
	}
	exec(`INSERT INTO raw_items (item_id, author_agent_id, raw_content, raw_url, created_at)
		VALUES (?, ?, 'complete broadcast body', 'https://example.test/broadcast', ?)`, itemID, owner, created)
	exec(`INSERT INTO processed_items (item_id, status, geo, updated_at)
		VALUES (?, 3, 'US', ?)`, itemID, created+1000)
	exec(`INSERT INTO item_stats (item_id, author_agent_id, consumed_count, score_1_count, score_2_count, total_score, created_at, updated_at)
		VALUES (?, ?, 32, 0, 1, 2, ?, ?)`, itemID, owner, created, created+2000)
	exec(`INSERT INTO agent_cards (agent_id, private_card, generated_at)
		VALUES (?, '{"geo":"Singapore","current_focus":"PRIVATE_CARD_MUST_NOT_LEAK"}'::jsonb, ?),
		       (?, '{"geo":"CN","human_status":"PRIVATE_CARD_MUST_NOT_LEAK"}'::jsonb, ?),
		       (?, '{"geo":"US","current_focus":"PRIVATE_CARD_MUST_NOT_LEAK"}'::jsonb, ?)`,
		owner, created, guest, created, helpful, created)
	exec(`INSERT INTO user_relations (from_uid, to_uid, rel_type, created_at)
		VALUES (?, ?, 1, ?)`, guest, helpful, created)
	for index, feedback := range []struct {
		agent int64
		score int
	}{
		{guest, -1}, {guest, 0}, {helpful, 2},
	} {
		exec(`INSERT INTO feedback_logs (stream_message_id, agent_id, item_id, score, feedback_at, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`, fmt.Sprintf("detail-%d-%d", base, index), feedback.agent,
			itemID, feedback.score, created+3000, created+3000)
	}

	request := func(viewer int64, expectedStatus int) map[string]interface{} {
		t.Helper()
		c := app.NewContext(1)
		id := strconv.FormatInt(itemID, 10)
		c.Request.SetRequestURI("/api/v1/items/" + id)
		c.Request.Header.SetMethod(http.MethodGet)
		c.Params = param.Params{{Key: "item_id", Value: id}}
		c.Set("agent_id", viewer)
		GetItem(context.Background(), c)
		if c.Response.StatusCode() != expectedStatus {
			t.Fatalf("status=%d body=%s", c.Response.StatusCode(), c.Response.Body())
		}
		if strings.Contains(string(c.Response.Body()), "PRIVATE_CARD_MUST_NOT_LEAK") {
			t.Fatal("private Card fields leaked")
		}
		var result struct {
			Data struct{ Item map[string]interface{} }
		}
		if err := json.Unmarshal(c.Response.Body(), &result); err != nil {
			t.Fatal(err)
		}
		return result.Data.Item
	}
	for _, viewer := range []int64{owner, guest} {
		item := request(viewer, http.StatusOK)
		if item["author_agent_id"] != strconv.FormatInt(owner, 10) || item["is_mine"] != (viewer == owner) || item["can_retract"] != (viewer == owner) {
			t.Fatalf("ownership mismatch: %#v", item)
		}
		if item["author_country_code"] != "SG" || item["country_code"] != "SG" || item["created_at"] != float64(created) || item["author_display_name_en"] != "Original Name" {
			t.Fatalf("author metadata mismatch: %#v", item)
		}
		viewerCountry := "SG"
		if viewer == guest {
			viewerCountry = "CN"
		}
		if item["viewer_country_code"] != viewerCountry {
			t.Fatalf("viewer location mismatch: %#v", item)
		}
		if item["consumed_count"] != float64(32) || item["praise_count"] != float64(1) || item["total_score"] != float64(2) {
			t.Fatalf("aggregate stats mismatch: %#v", item)
		}
		interactions := item["recent_interactions"].([]interface{})
		if len(interactions) != 1 || item["interaction_total"] != float64(1) {
			t.Fatalf("positive roster mismatch: %#v", item)
		}
		interaction := interactions[0].(map[string]interface{})
		if interaction["agent_id"] != strconv.FormatInt(helpful, 10) || interaction["country_code"] != "US" || interaction["score"] != float64(2) || interaction["is_friend"] != (viewer == guest) {
			t.Fatalf("roster scope or viewer relationship mismatch: %#v", interaction)
		}
		if viewer == guest {
			if item["my_score"] != float64(0) || item["feedback_at"] != float64(created+3000) {
				t.Fatalf("latest neutral feedback was lost: %#v", item)
			}
		} else if item["my_score"] != nil || item["feedback_at"] != nil {
			t.Fatalf("another viewer's feedback leaked: %#v", item)
		}
	}
	exec(`UPDATE agent_cards SET private_card = '{"geo":""}'::jsonb WHERE agent_id = ?`, owner)
	if item := request(guest, http.StatusOK); item["author_country_code"] != "" || item["country_code"] != "" {
		t.Fatalf("cleared country inherited broadcast geo: %#v", item)
	}
	for _, status := range []int{0, 1, 2, 4, 5} {
		exec(`UPDATE processed_items SET status = ? WHERE item_id = ?`, status, itemID)
		if item := request(guest, http.StatusNotFound); item != nil {
			t.Fatalf("non-public broadcast data leaked: %#v", item)
		}
		item := request(owner, http.StatusOK)
		if item["is_mine"] != true || item["can_retract"] != (status >= 0 && status <= 3) || item["status"] != float64(status) || item["retracted"] != (status == 5) {
			t.Fatalf("owner's non-public detail mismatch: %#v", item)
		}
	}
}
