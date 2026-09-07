package pm

import (
	"fmt"
	"net/url"
	"strconv"
	"testing"
	"time"

	"eigenflux_server/tests/testutil"
)

func TestListConversationsStableCursorAcrossEqualTimestamps(t *testing.T) {
	testutil.WaitForAPI(t)
	emails := []string{
		"pm_cursor_viewer@test.com",
		"pm_cursor_peer_a@test.com",
		"pm_cursor_peer_b@test.com",
		"pm_cursor_peer_c@test.com",
	}
	testutil.CleanupTestEmails(t, emails...)
	viewer := testutil.RegisterAgent(t, emails[0], "Cursor Viewer", "cursor pagination test")
	peerA := testutil.RegisterAgent(t, emails[1], "Cursor Peer A", "cursor pagination test")
	peerB := testutil.RegisterAgent(t, emails[2], "Cursor Peer B", "cursor pagination test")
	peerC := testutil.RegisterAgent(t, emails[3], "Cursor Peer C", "cursor pagination test")
	defer testutil.CleanupTestEmails(t, emails...)

	viewerID, _ := strconv.ParseInt(viewer["agent_id"].(string), 10, 64)
	peerIDs := make([]int64, 0, 3)
	for _, peer := range []map[string]interface{}{peerA, peerB, peerC} {
		peerID, _ := strconv.ParseInt(peer["agent_id"].(string), 10, 64)
		peerIDs = append(peerIDs, peerID)
	}
	cleanPMData(t, append([]int64{viewerID}, peerIDs...)...)
	defer cleanPMData(t, append([]int64{viewerID}, peerIDs...)...)

	baseID := time.Now().UnixNano()
	updatedAt := time.Now().UnixMilli()
	wantConvIDs := []int64{baseID + 3, baseID + 2, baseID + 1}
	for i, convID := range wantConvIDs {
		peerID := peerIDs[i]
		if _, err := testutil.TestDB.Exec(
			`INSERT INTO conversations
			 (conv_id, participant_a, participant_b, initiator_id, last_sender_id, origin_type, origin_id, msg_count, status, updated_at, participant_a_name, participant_b_name)
			 VALUES ($1, $2, $3, $2, $2, 'friend', 0, 1, 0, $4, 'Cursor Viewer', $5)`,
			convID, viewerID, peerID, updatedAt, fmt.Sprintf("Cursor Peer %d", i+1),
		); err != nil {
			t.Fatalf("insert conversation %d: %v", convID, err)
		}
		if _, err := testutil.TestDB.Exec(
			`INSERT INTO private_messages
			 (msg_id, conv_id, sender_id, receiver_id, content, is_read, created_at, sender_name, receiver_name)
			 VALUES ($1, $2, $3, $4, $5, true, $6, 'Cursor Viewer', $7)`,
			baseID+100+int64(i), convID, viewerID, peerID, fmt.Sprintf("cursor message %d", i+1), updatedAt, fmt.Sprintf("Cursor Peer %d", i+1),
		); err != nil {
			t.Fatalf("insert message for conversation %d: %v", convID, err)
		}
	}

	token := viewer["token"].(string)
	cursor := ""
	gotConvIDs := make([]int64, 0, len(wantConvIDs))
	for page := 0; page < len(wantConvIDs); page++ {
		path := "/api/v1/pm/conversations?origin_type=friend&limit=1"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		resp := testutil.DoGet(t, path, token)
		if code := int(resp["code"].(float64)); code != 0 {
			t.Fatalf("page %d failed: code=%d msg=%v", page+1, code, resp["msg"])
		}
		data := resp["data"].(map[string]interface{})
		conversations := data["conversations"].([]interface{})
		if len(conversations) != 1 {
			t.Fatalf("page %d returned %d conversations, want 1", page+1, len(conversations))
		}
		convID, _ := strconv.ParseInt(conversations[0].(map[string]interface{})["conv_id"].(string), 10, 64)
		gotConvIDs = append(gotConvIDs, convID)
		cursor, _ = data["next_cursor_v2"].(string)
		if cursor == "" {
			t.Fatalf("page %d returned an empty next_cursor_v2", page+1)
		}
	}
	for i := range wantConvIDs {
		if gotConvIDs[i] != wantConvIDs[i] {
			t.Fatalf("conversation order = %v, want %v", gotConvIDs, wantConvIDs)
		}
	}
}
