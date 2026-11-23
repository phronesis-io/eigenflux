package consolev2

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"eigenflux_server/pkg/agentidentity"
)

func TestPostgresFriendConversationOpening(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN is required for friend conversation opening tests")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	exec := func(sql string, args ...interface{}) {
		t.Helper()
		if err := tx.Exec(sql, args...).Error; err != nil {
			t.Fatal(err)
		}
	}
	base, now := time.Now().UnixNano(), time.Now().UnixMilli()
	sender, recipient, outsider := base+1, base+2, base+3
	requestID, directID, broadcastID := base+4, base+5, base+6
	greeting := strings.Repeat("申请A", 80)
	for _, id := range []int64{sender, recipient, outsider} {
		shortID, err := agentidentity.GenerateShortID()
		if err != nil {
			t.Fatal(err)
		}
		exec(`INSERT INTO agents(agent_id, short_id, email, agent_name, bio, created_at, updated_at)
			VALUES(?, ?, ?, 'Opening participant', '', ?, ?)`,
			id, shortID, fmt.Sprintf("opening-%d@example.test", id), now, now)
	}
	viewer := sender
	svc := &Service{db: tx}
	h := server.New()
	asViewer := func(handler app.HandlerFunc) app.HandlerFunc {
		return func(ctx context.Context, c *app.RequestContext) {
			c.Set("agent_id", viewer)
			handler(ctx, c)
		}
	}
	h.GET("/friends", asViewer(svc.listCommunicationFriends))
	h.GET("/conversations/:conv_id/messages", asViewer(svc.listCommunicationMessages))
	get := func(path string) map[string]interface{} {
		t.Helper()
		status, body, _ := performJSON(t, h, http.MethodGet, path, nil)
		if status != 200 {
			t.Fatalf("GET %s: %d %#v", path, status, body)
		}
		return responseData(t, body)
	}
	friend := func() map[string]interface{} {
		t.Helper()
		rows := get("/friends")["friends"].([]interface{})
		if len(rows) != 1 {
			t.Fatalf("want one contact: %#v", rows)
		}
		return rows[0].(map[string]interface{})
	}
	checkNote := func(data map[string]interface{}) {
		t.Helper()
		note, ok := data["friend_request_note"].(map[string]interface{})
		if !ok || note["request_id"] != strconv.FormatInt(requestID, 10) ||
			note["sender_agent_id"] != strconv.FormatInt(sender, 10) || note["greeting"] != greeting ||
			note["created_at"] != float64(now) || len(note) != 4 {
			t.Fatalf("unexpected note: %#v", note)
		}
	}
	exec(`INSERT INTO user_relations(from_uid, to_uid, rel_type, created_at) VALUES(?,?,1,?),(?,?,1,?)`, sender, recipient, now, recipient, sender, now)
	exec(`INSERT INTO friend_requests(id, from_uid, to_uid, status, greeting, remark, created_at, updated_at)
		VALUES(?,?,?,0,?,'sender-private-remark',?,?)`, requestID, sender, recipient, greeting, now, now)
	if row := friend(); row["friend_request_note"] != nil {
		t.Fatalf("pending greeting leaked: %#v", row)
	}
	exec(`UPDATE friend_requests SET status=1 WHERE id=?`, requestID)
	for _, viewer = range []int64{sender, recipient} {
		row := friend()
		checkNote(row)
		if conv, present := row["conv_id"]; !present || conv != nil || row["last_message"] != nil {
			t.Fatalf("request created a message or thread: %#v", row)
		}
	}
	for _, thread := range []struct {
		id, origin int64
		kind       string
	}{{directID, 0, "friend"}, {broadcastID, base + 7, "broadcast"}} {
		exec(`INSERT INTO conversations(conv_id, participant_a, participant_b, initiator_id, last_sender_id, origin_type, origin_id, updated_at)
			VALUES(?,?,?,?,?,?,?,?)`, thread.id, sender, recipient, sender, sender, thread.kind, thread.origin, now)
	}
	for i := int64(1); i <= 2; i++ {
		exec(`INSERT INTO private_messages(msg_id, conv_id, sender_id, receiver_id, content, is_read, created_at)
			VALUES(?,?,?,?,'real message',false,?)`, base+10+i, directID, sender, recipient, now+i)
	}
	history := fmt.Sprintf("/conversations/%d/messages?limit=1", directID)
	for _, viewer = range []int64{sender, recipient} {
		row := friend()
		checkNote(row)
		if row["conv_id"] != strconv.FormatInt(directID, 10) || row["last_message"].(map[string]interface{})["content"] != "real message" {
			t.Fatalf("contact mapped to wrong conversation: %#v", row)
		}
		page := get(history)
		checkNote(page)
		if len(page["messages"].([]interface{})) != 1 || page["has_more"] != true {
			t.Fatalf("unexpected first page: %#v", page)
		}
		last := get(history + "&cursor=" + page["next_cursor"].(string))
		checkNote(last)
		if len(last["messages"].([]interface{})) != 1 || last["has_more"] != false {
			t.Fatalf("unexpected second page: %#v", last)
		}
		if get(fmt.Sprintf("/conversations/%d/messages", broadcastID))["friend_request_note"] != nil {
			t.Fatal("direct greeting leaked into a broadcast thread")
		}
	}
	viewer = outsider
	status, _, _ := performJSON(t, h, http.MethodGet, history, nil)
	if status != http.StatusNotFound {
		t.Fatalf("outsider could open conversation: %d", status)
	}
	viewer = recipient
	exec(`UPDATE friend_requests SET status=4 WHERE id=?`, requestID)
	exec(`DELETE FROM user_relations WHERE (from_uid=? AND to_uid=?) OR (from_uid=? AND to_uid=?)`, sender, recipient, recipient, sender)
	checkNote(get(history))
	exec(`UPDATE friend_requests SET id=?, status=0, greeting='replacement', created_at=? WHERE id=?`, base+20, now+10, requestID)
	exec(`UPDATE friend_requests SET status=1 WHERE id=?`, base+20)
	checkNote(get(history))
	var unread int64
	if err := tx.Raw(`SELECT count(*) FROM private_messages WHERE conv_id=? AND NOT is_read`, directID).Scan(&unread).Error; err != nil || unread != 2 {
		t.Fatalf("read path affected unread messages: %d, %v", unread, err)
	}
}
