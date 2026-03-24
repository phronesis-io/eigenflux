package pm

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"testing"
	"time"

	"eigenflux_server/tests/testutil"
)

func cleanRelationsData(t *testing.T, agentIDs ...int64) {
	t.Helper()
	ctx := context.Background()
	rdb := testutil.GetTestRedis()

	for _, id := range agentIDs {
		testutil.TestDB.Exec("DELETE FROM user_relations WHERE from_uid = $1 OR to_uid = $1", id)
		testutil.TestDB.Exec("DELETE FROM friend_requests WHERE from_uid = $1 OR to_uid = $1", id)
		rdb.Del(ctx, fmt.Sprintf("friend:%d", id))
		rdb.Del(ctx, fmt.Sprintf("block:%d", id))
		rdb.Del(ctx, fmt.Sprintf("friend_count:%d", id))
		rdb.Del(ctx, fmt.Sprintf("pm:notify:%d", id))
	}
}

// Test 1: Normal friend request flow
func TestSendFriendRequest_Success(t *testing.T) {
	testutil.WaitForAPI(t)
	emails := []string{"friend_a@test.com", "friend_b@test.com"}
	testutil.CleanupTestEmails(t, emails...)

	agentA := testutil.RegisterAgent(t, "friend_a@test.com", "Agent A", "bio")
	agentB := testutil.RegisterAgent(t, "friend_b@test.com", "Agent B", "bio")

	uidA, _ := strconv.ParseInt(agentA["agent_id"].(string), 10, 64)
	uidB, _ := strconv.ParseInt(agentB["agent_id"].(string), 10, 64)
	defer cleanRelationsData(t, uidA, uidB)

	resp := testutil.DoPost(t, "/api/v1/relations/apply", map[string]string{
		"from_uid": agentA["agent_id"].(string),
		"to_uid":   agentB["agent_id"].(string),
	}, agentA["token"].(string))

	code := int(resp["code"].(float64))
	if code != 0 {
		t.Fatalf("SendFriendRequest failed: code=%d msg=%v", code, resp["msg"])
	}
	data := resp["data"].(map[string]interface{})
	if _, ok := data["request_id"].(string); !ok {
		t.Fatalf("expected request_id as string")
	}
	t.Logf("Friend request sent: request_id=%s", data["request_id"])
}

// Test 2: Mutual pending auto-accept
func TestSendFriendRequest_MutualPending(t *testing.T) {
	testutil.WaitForAPI(t)
	emails := []string{"mutual_a@test.com", "mutual_b@test.com"}
	testutil.CleanupTestEmails(t, emails...)

	agentA := testutil.RegisterAgent(t, "mutual_a@test.com", "Mutual A", "bio")
	agentB := testutil.RegisterAgent(t, "mutual_b@test.com", "Mutual B", "bio")

	uidA, _ := strconv.ParseInt(agentA["agent_id"].(string), 10, 64)
	uidB, _ := strconv.ParseInt(agentB["agent_id"].(string), 10, 64)
	defer cleanRelationsData(t, uidA, uidB)

	// A sends request to B
	testutil.DoPost(t, "/api/v1/relations/apply", map[string]string{
		"from_uid": agentA["agent_id"].(string),
		"to_uid":   agentB["agent_id"].(string),
	}, agentA["token"].(string))

	// B sends request to A - should auto-accept
	resp := testutil.DoPost(t, "/api/v1/relations/apply", map[string]string{
		"from_uid": agentB["agent_id"].(string),
		"to_uid":   agentA["agent_id"].(string),
	}, agentB["token"].(string))

	code := int(resp["code"].(float64))
	if code != 0 {
		t.Fatalf("Mutual request failed: code=%d msg=%v", code, resp["msg"])
	}

	// Verify friendship exists in DB
	var count int64
	err := testutil.TestDB.QueryRow("SELECT COUNT(*) FROM user_relations WHERE from_uid = $1 AND to_uid = $2 AND rel_type = 1", uidA, uidB).Scan(&count)
	if err != nil {
		t.Fatalf("failed to query DB: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 friend relation A→B, got %d", count)
	}
	t.Logf("Mutual request auto-accepted, friendship created")
}

// Test 3: Accept friend request
func TestHandleFriendRequest_Accept(t *testing.T) {
	testutil.WaitForAPI(t)
	emails := []string{"accept_a@test.com", "accept_b@test.com"}
	testutil.CleanupTestEmails(t, emails...)

	agentA := testutil.RegisterAgent(t, "accept_a@test.com", "Accept A", "bio")
	agentB := testutil.RegisterAgent(t, "accept_b@test.com", "Accept B", "bio")

	uidA, _ := strconv.ParseInt(agentA["agent_id"].(string), 10, 64)
	uidB, _ := strconv.ParseInt(agentB["agent_id"].(string), 10, 64)
	defer cleanRelationsData(t, uidA, uidB)

	// A sends request to B
	resp := testutil.DoPost(t, "/api/v1/relations/apply", map[string]string{
		"from_uid": agentA["agent_id"].(string),
		"to_uid":   agentB["agent_id"].(string),
	}, agentA["token"].(string))
	requestID := resp["data"].(map[string]interface{})["request_id"].(string)

	// B accepts
	resp = testutil.DoPost(t, "/api/v1/relations/handle", map[string]interface{}{
		"agent_id":   agentB["agent_id"].(string),
		"request_id": requestID,
		"action":     1, // ACCEPT
	}, agentB["token"].(string))

	code := int(resp["code"].(float64))
	if code != 0 {
		t.Fatalf("Accept failed: code=%d msg=%v", code, resp["msg"])
	}

	// Verify 2 friend rows created
	var count int64
	err := testutil.TestDB.QueryRow("SELECT COUNT(*) FROM user_relations WHERE ((from_uid = $1 AND to_uid = $2) OR (from_uid = $2 AND to_uid = $1)) AND rel_type = 1", uidA, uidB).Scan(&count)
	if err != nil {
		t.Fatalf("failed to query DB: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 friend relations, got %d", count)
	}
	t.Logf("Friend request accepted, 2 symmetric rows created")
}

// Test 4: Reject friend request
func TestHandleFriendRequest_Reject(t *testing.T) {
	testutil.WaitForAPI(t)
	emails := []string{"reject_a@test.com", "reject_b@test.com"}
	testutil.CleanupTestEmails(t, emails...)

	agentA := testutil.RegisterAgent(t, "reject_a@test.com", "Reject A", "bio")
	agentB := testutil.RegisterAgent(t, "reject_b@test.com", "Reject B", "bio")

	uidA, _ := strconv.ParseInt(agentA["agent_id"].(string), 10, 64)
	uidB, _ := strconv.ParseInt(agentB["agent_id"].(string), 10, 64)
	defer cleanRelationsData(t, uidA, uidB)

	// A sends request to B
	resp := testutil.DoPost(t, "/api/v1/relations/apply", map[string]string{
		"from_uid": agentA["agent_id"].(string),
		"to_uid":   agentB["agent_id"].(string),
	}, agentA["token"].(string))
	requestID := resp["data"].(map[string]interface{})["request_id"].(string)

	// B rejects
	resp = testutil.DoPost(t, "/api/v1/relations/handle", map[string]interface{}{
		"agent_id":   agentB["agent_id"].(string),
		"request_id": requestID,
		"action":     2, // REJECT
	}, agentB["token"].(string))

	code := int(resp["code"].(float64))
	if code != 0 {
		t.Fatalf("Reject failed: code=%d msg=%v", code, resp["msg"])
	}

	// Verify no friend rows created
	var count int64
	err := testutil.TestDB.QueryRow("SELECT COUNT(*) FROM user_relations WHERE from_uid = $1 AND to_uid = $2 AND rel_type = 1", uidA, uidB).Scan(&count)
	if err != nil {
		t.Fatalf("failed to query DB: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 friend relations after reject, got %d", count)
	}

	// Verify request status is rejected
	var status int16
	err = testutil.TestDB.QueryRow("SELECT status FROM friend_requests WHERE id = $1", requestID).Scan(&status)
	if err != nil {
		t.Fatalf("failed to query request status: %v", err)
	}
	if status != 2 {
		t.Fatalf("expected status=2 (rejected), got %d", status)
	}
	t.Logf("Friend request rejected, no friendship created")
}

// Test 5: Cancel own request
func TestHandleFriendRequest_Cancel(t *testing.T) {
	testutil.WaitForAPI(t)
	emails := []string{"cancel_a@test.com", "cancel_b@test.com"}
	testutil.CleanupTestEmails(t, emails...)

	agentA := testutil.RegisterAgent(t, "cancel_a@test.com", "Cancel A", "bio")
	agentB := testutil.RegisterAgent(t, "cancel_b@test.com", "Cancel B", "bio")

	uidA, _ := strconv.ParseInt(agentA["agent_id"].(string), 10, 64)
	uidB, _ := strconv.ParseInt(agentB["agent_id"].(string), 10, 64)
	defer cleanRelationsData(t, uidA, uidB)

	// A sends request to B
	resp := testutil.DoPost(t, "/api/v1/relations/apply", map[string]string{
		"from_uid": agentA["agent_id"].(string),
		"to_uid":   agentB["agent_id"].(string),
	}, agentA["token"].(string))
	requestID := resp["data"].(map[string]interface{})["request_id"].(string)

	// A cancels own request
	resp = testutil.DoPost(t, "/api/v1/relations/handle", map[string]interface{}{
		"agent_id":   agentA["agent_id"].(string),
		"request_id": requestID,
		"action":     3, // CANCEL
	}, agentA["token"].(string))

	code := int(resp["code"].(float64))
	if code != 0 {
		t.Fatalf("Cancel failed: code=%d msg=%v", code, resp["msg"])
	}

	// Verify request status is cancelled
	var status int16
	err := testutil.TestDB.QueryRow("SELECT status FROM friend_requests WHERE id = $1", requestID).Scan(&status)
	if err != nil {
		t.Fatalf("failed to query request status: %v", err)
	}
	if status != 3 {
		t.Fatalf("expected status=3 (cancelled), got %d", status)
	}
	t.Logf("Friend request cancelled by sender")
}

// Test 6: Unfriend
func TestUnfriend_Success(t *testing.T) {
	testutil.WaitForAPI(t)
	emails := []string{"unfriend_a@test.com", "unfriend_b@test.com"}
	testutil.CleanupTestEmails(t, emails...)

	agentA := testutil.RegisterAgent(t, "unfriend_a@test.com", "Unfriend A", "bio")
	agentB := testutil.RegisterAgent(t, "unfriend_b@test.com", "Unfriend B", "bio")

	uidA, _ := strconv.ParseInt(agentA["agent_id"].(string), 10, 64)
	uidB, _ := strconv.ParseInt(agentB["agent_id"].(string), 10, 64)
	defer cleanRelationsData(t, uidA, uidB)

	// Create friendship
	resp := testutil.DoPost(t, "/api/v1/relations/apply", map[string]string{
		"from_uid": agentA["agent_id"].(string),
		"to_uid":   agentB["agent_id"].(string),
	}, agentA["token"].(string))
	requestID := resp["data"].(map[string]interface{})["request_id"].(string)
	testutil.DoPost(t, "/api/v1/relations/handle", map[string]interface{}{
		"agent_id":   agentB["agent_id"].(string),
		"request_id": requestID,
		"action":     1,
	}, agentB["token"].(string))

	// A unfriends B
	resp = testutil.DoPost(t, "/api/v1/relations/unfriend", map[string]string{
		"from_uid": agentA["agent_id"].(string),
		"to_uid":   agentB["agent_id"].(string),
	}, agentA["token"].(string))

	code := int(resp["code"].(float64))
	if code != 0 {
		t.Fatalf("Unfriend failed: code=%d msg=%v", code, resp["msg"])
	}

	// Verify friend rows deleted
	var count int64
	err := testutil.TestDB.QueryRow("SELECT COUNT(*) FROM user_relations WHERE ((from_uid = $1 AND to_uid = $2) OR (from_uid = $2 AND to_uid = $1)) AND rel_type = 1", uidA, uidB).Scan(&count)
	if err != nil {
		t.Fatalf("failed to query DB: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 friend relations after unfriend, got %d", count)
	}

	// Verify request status updated to unfriended
	var status int16
	err = testutil.TestDB.QueryRow("SELECT status FROM friend_requests WHERE id = $1", requestID).Scan(&status)
	if err != nil {
		t.Fatalf("failed to query request status: %v", err)
	}
	if status != 4 {
		t.Fatalf("expected status=4 (unfriended), got %d", status)
	}
	t.Logf("Unfriend successful, 2 rows deleted, request status updated")
}

// Test 7: Block user
func TestBlockUser_Success(t *testing.T) {
	testutil.WaitForAPI(t)
	emails := []string{"block_a@test.com", "block_b@test.com"}
	testutil.CleanupTestEmails(t, emails...)

	agentA := testutil.RegisterAgent(t, "block_a@test.com", "Block A", "bio")
	agentB := testutil.RegisterAgent(t, "block_b@test.com", "Block B", "bio")

	uidA, _ := strconv.ParseInt(agentA["agent_id"].(string), 10, 64)
	uidB, _ := strconv.ParseInt(agentB["agent_id"].(string), 10, 64)
	defer cleanRelationsData(t, uidA, uidB)

	// A blocks B
	resp := testutil.DoPost(t, "/api/v1/relations/block", map[string]string{
		"from_uid": agentA["agent_id"].(string),
		"to_uid":   agentB["agent_id"].(string),
	}, agentA["token"].(string))

	code := int(resp["code"].(float64))
	if code != 0 {
		t.Fatalf("Block failed: code=%d msg=%v", code, resp["msg"])
	}

	// Verify block row created
	var count int64
	err := testutil.TestDB.QueryRow("SELECT COUNT(*) FROM user_relations WHERE from_uid = $1 AND to_uid = $2 AND rel_type = 2", uidA, uidB).Scan(&count)
	if err != nil {
		t.Fatalf("failed to query DB: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 block relation, got %d", count)
	}
	t.Logf("Block successful")
}

// Test 8: Block removes friendship
func TestBlockUser_RemovesFriendship(t *testing.T) {
	testutil.WaitForAPI(t)
	emails := []string{"blockfriend_a@test.com", "blockfriend_b@test.com"}
	testutil.CleanupTestEmails(t, emails...)

	agentA := testutil.RegisterAgent(t, "blockfriend_a@test.com", "BlockFriend A", "bio")
	agentB := testutil.RegisterAgent(t, "blockfriend_b@test.com", "BlockFriend B", "bio")

	uidA, _ := strconv.ParseInt(agentA["agent_id"].(string), 10, 64)
	uidB, _ := strconv.ParseInt(agentB["agent_id"].(string), 10, 64)
	defer cleanRelationsData(t, uidA, uidB)

	// Create friendship first
	resp := testutil.DoPost(t, "/api/v1/relations/apply", map[string]string{
		"from_uid": agentA["agent_id"].(string),
		"to_uid":   agentB["agent_id"].(string),
	}, agentA["token"].(string))
	requestID := resp["data"].(map[string]interface{})["request_id"].(string)
	testutil.DoPost(t, "/api/v1/relations/handle", map[string]interface{}{
		"agent_id":   agentB["agent_id"].(string),
		"request_id": requestID,
		"action":     1,
	}, agentB["token"].(string))

	// A blocks B
	testutil.DoPost(t, "/api/v1/relations/block", map[string]string{
		"from_uid": agentA["agent_id"].(string),
		"to_uid":   agentB["agent_id"].(string),
	}, agentA["token"].(string))

	// Verify friendship deleted
	var count int64
	err := testutil.TestDB.QueryRow("SELECT COUNT(*) FROM user_relations WHERE ((from_uid = $1 AND to_uid = $2) OR (from_uid = $2 AND to_uid = $1)) AND rel_type = 1", uidA, uidB).Scan(&count)
	if err != nil {
		t.Fatalf("failed to query DB: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 friend relations after block, got %d", count)
	}
	t.Logf("Block removed friendship")
}

// Test 9: Unblock user
func TestUnblockUser_Success(t *testing.T) {
	testutil.WaitForAPI(t)
	emails := []string{"unblock_a@test.com", "unblock_b@test.com"}
	testutil.CleanupTestEmails(t, emails...)

	agentA := testutil.RegisterAgent(t, "unblock_a@test.com", "Unblock A", "bio")
	agentB := testutil.RegisterAgent(t, "unblock_b@test.com", "Unblock B", "bio")

	uidA, _ := strconv.ParseInt(agentA["agent_id"].(string), 10, 64)
	uidB, _ := strconv.ParseInt(agentB["agent_id"].(string), 10, 64)
	defer cleanRelationsData(t, uidA, uidB)

	// A blocks B
	testutil.DoPost(t, "/api/v1/relations/block", map[string]string{
		"from_uid": agentA["agent_id"].(string),
		"to_uid":   agentB["agent_id"].(string),
	}, agentA["token"].(string))

	// A unblocks B
	resp := testutil.DoPost(t, "/api/v1/relations/unblock", map[string]string{
		"from_uid": agentA["agent_id"].(string),
		"to_uid":   agentB["agent_id"].(string),
	}, agentA["token"].(string))

	code := int(resp["code"].(float64))
	if code != 0 {
		t.Fatalf("Unblock failed: code=%d msg=%v", code, resp["msg"])
	}

	// Verify block row deleted
	var count int64
	err := testutil.TestDB.QueryRow("SELECT COUNT(*) FROM user_relations WHERE from_uid = $1 AND to_uid = $2 AND rel_type = 2", uidA, uidB).Scan(&count)
	if err != nil {
		t.Fatalf("failed to query DB: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 block relations after unblock, got %d", count)
	}
	t.Logf("Unblock successful")
}

// Test 10: Friend PM requires friendship
func TestSendPM_FriendBased_RequiresFriendship(t *testing.T) {
	testutil.WaitForAPI(t)
	emails := []string{"friendpm_a@test.com", "friendpm_b@test.com"}
	testutil.CleanupTestEmails(t, emails...)

	agentA := testutil.RegisterAgent(t, "friendpm_a@test.com", "FriendPM A", "bio")
	agentB := testutil.RegisterAgent(t, "friendpm_b@test.com", "FriendPM B", "bio")

	uidA, _ := strconv.ParseInt(agentA["agent_id"].(string), 10, 64)
	uidB, _ := strconv.ParseInt(agentB["agent_id"].(string), 10, 64)
	defer cleanRelationsData(t, uidA, uidB)

	// A tries to send friend PM to B without friendship
	resp := testutil.DoPost(t, "/api/v1/pm/send", map[string]string{
		"receiver_id": agentB["agent_id"].(string),
		"content":     "Friend PM without friendship",
	}, agentA["token"].(string))

	code := int(resp["code"].(float64))
	if code != 403 {
		t.Fatalf("expected code=403 (not friends), got code=%d", code)
	}
	t.Logf("Friend PM correctly rejected without friendship")
}

// Test 11: Blocked user PM silent success
func TestSendPM_BlockedUser_SilentSuccess(t *testing.T) {
	testutil.WaitForAPI(t)
	emails := []string{"blockpm_a@test.com", "blockpm_b@test.com"}
	testutil.CleanupTestEmails(t, emails...)

	agentA := testutil.RegisterAgent(t, "blockpm_a@test.com", "BlockPM A", "bio")
	agentB := testutil.RegisterAgent(t, "blockpm_b@test.com", "BlockPM B", "bio")

	uidA, _ := strconv.ParseInt(agentA["agent_id"].(string), 10, 64)
	uidB, _ := strconv.ParseInt(agentB["agent_id"].(string), 10, 64)
	defer cleanRelationsData(t, uidA, uidB)

	// Create friendship
	resp := testutil.DoPost(t, "/api/v1/relations/apply", map[string]string{
		"from_uid": agentA["agent_id"].(string),
		"to_uid":   agentB["agent_id"].(string),
	}, agentA["token"].(string))
	requestID := resp["data"].(map[string]interface{})["request_id"].(string)
	testutil.DoPost(t, "/api/v1/relations/handle", map[string]interface{}{
		"agent_id":   agentB["agent_id"].(string),
		"request_id": requestID,
		"action":     1,
	}, agentB["token"].(string))

	// B blocks A
	testutil.DoPost(t, "/api/v1/relations/block", map[string]string{
		"from_uid": agentB["agent_id"].(string),
		"to_uid":   agentA["agent_id"].(string),
	}, agentB["token"].(string))

	// A tries to send PM to B - should get success but no delivery
	resp = testutil.DoPost(t, "/api/v1/pm/send", map[string]string{
		"receiver_id": agentB["agent_id"].(string),
		"content":     "PM to blocked user",
	}, agentA["token"].(string))

	code := int(resp["code"].(float64))
	if code != 0 {
		t.Fatalf("expected code=0 (silent success), got code=%d", code)
	}

	// Verify no message was actually delivered
	resp = testutil.DoGet(t, "/api/v1/pm/fetch", agentB["token"].(string))
	data := resp["data"].(map[string]interface{})
	messages := data["messages"].([]interface{})
	if len(messages) != 0 {
		t.Fatalf("expected 0 messages delivered to blocked user, got %d", len(messages))
	}
	t.Logf("Blocked user PM returned success but no delivery")
}

// Test 12: Cannot send request when blocked
func TestSendFriendRequest_BlockedByReceiver(t *testing.T) {
	testutil.WaitForAPI(t)
	emails := []string{"blockreq_a@test.com", "blockreq_b@test.com"}
	testutil.CleanupTestEmails(t, emails...)

	agentA := testutil.RegisterAgent(t, "blockreq_a@test.com", "BlockReq A", "bio")
	agentB := testutil.RegisterAgent(t, "blockreq_b@test.com", "BlockReq B", "bio")

	uidA, _ := strconv.ParseInt(agentA["agent_id"].(string), 10, 64)
	uidB, _ := strconv.ParseInt(agentB["agent_id"].(string), 10, 64)
	defer cleanRelationsData(t, uidA, uidB)

	// B blocks A
	testutil.DoPost(t, "/api/v1/relations/block", map[string]string{
		"from_uid": agentB["agent_id"].(string),
		"to_uid":   agentA["agent_id"].(string),
	}, agentB["token"].(string))

	// A tries to send request to B
	resp := testutil.DoPost(t, "/api/v1/relations/apply", map[string]string{
		"from_uid": agentA["agent_id"].(string),
		"to_uid":   agentB["agent_id"].(string),
	}, agentA["token"].(string))

	code := int(resp["code"].(float64))
	if code != 403 {
		t.Fatalf("expected code=403 (blocked), got code=%d", code)
	}
	t.Logf("Friend request correctly rejected when blocked")
}

// Test 13: Wrong person cannot accept request
func TestHandleFriendRequest_WrongPersonAccept(t *testing.T) {
	testutil.WaitForAPI(t)
	emails := []string{"wrong_a@test.com", "wrong_b@test.com", "wrong_c@test.com"}
	testutil.CleanupTestEmails(t, emails...)

	agentA := testutil.RegisterAgent(t, "wrong_a@test.com", "Wrong A", "bio")
	agentB := testutil.RegisterAgent(t, "wrong_b@test.com", "Wrong B", "bio")
	agentC := testutil.RegisterAgent(t, "wrong_c@test.com", "Wrong C", "bio")

	uidA, _ := strconv.ParseInt(agentA["agent_id"].(string), 10, 64)
	uidB, _ := strconv.ParseInt(agentB["agent_id"].(string), 10, 64)
	uidC, _ := strconv.ParseInt(agentC["agent_id"].(string), 10, 64)
	defer cleanRelationsData(t, uidA, uidB, uidC)

	// A sends request to B
	resp := testutil.DoPost(t, "/api/v1/relations/apply", map[string]string{
		"from_uid": agentA["agent_id"].(string),
		"to_uid":   agentB["agent_id"].(string),
	}, agentA["token"].(string))
	requestID := resp["data"].(map[string]interface{})["request_id"].(string)

	// C tries to accept A→B request
	resp = testutil.DoPost(t, "/api/v1/relations/handle", map[string]interface{}{
		"agent_id":   agentC["agent_id"].(string),
		"request_id": requestID,
		"action":     1,
	}, agentC["token"].(string))

	code := int(resp["code"].(float64))
	if code != 403 {
		t.Fatalf("expected code=403 (not recipient), got code=%d", code)
	}
	t.Logf("Wrong person correctly rejected from accepting request")
}

// Test 14: List friend requests
func TestListFriendRequests_Success(t *testing.T) {
	testutil.WaitForAPI(t)
	emails := []string{"listreq_a@test.com", "listreq_b@test.com"}
	testutil.CleanupTestEmails(t, emails...)

	agentA := testutil.RegisterAgent(t, "listreq_a@test.com", "ListReq A", "bio")
	agentB := testutil.RegisterAgent(t, "listreq_b@test.com", "ListReq B", "bio")

	uidA, _ := strconv.ParseInt(agentA["agent_id"].(string), 10, 64)
	uidB, _ := strconv.ParseInt(agentB["agent_id"].(string), 10, 64)
	defer cleanRelationsData(t, uidA, uidB)

	// A sends request to B
	testutil.DoPost(t, "/api/v1/relations/apply", map[string]string{
		"from_uid": agentA["agent_id"].(string),
		"to_uid":   agentB["agent_id"].(string),
	}, agentA["token"].(string))

	// B lists incoming requests
	resp := testutil.DoGet(t, "/api/v1/relations/applications?direction=incoming", agentB["token"].(string))
	code := int(resp["code"].(float64))
	if code != 0 {
		t.Fatalf("ListFriendRequests failed: code=%d msg=%v", code, resp["msg"])
	}

	data := resp["data"].(map[string]interface{})
	requests := data["requests"].([]interface{})
	if len(requests) != 1 {
		t.Fatalf("expected 1 incoming request, got %d", len(requests))
	}
	t.Logf("List friend requests successful")
}

// Test 15: List friends
func TestListFriends_Success(t *testing.T) {
	testutil.WaitForAPI(t)
	emails := []string{"listfriend_a@test.com", "listfriend_b@test.com"}
	testutil.CleanupTestEmails(t, emails...)

	agentA := testutil.RegisterAgent(t, "listfriend_a@test.com", "ListFriend A", "bio")
	agentB := testutil.RegisterAgent(t, "listfriend_b@test.com", "ListFriend B", "bio")

	uidA, _ := strconv.ParseInt(agentA["agent_id"].(string), 10, 64)
	uidB, _ := strconv.ParseInt(agentB["agent_id"].(string), 10, 64)
	defer cleanRelationsData(t, uidA, uidB)

	// Create friendship
	resp := testutil.DoPost(t, "/api/v1/relations/apply", map[string]string{
		"from_uid": agentA["agent_id"].(string),
		"to_uid":   agentB["agent_id"].(string),
	}, agentA["token"].(string))
	requestID := resp["data"].(map[string]interface{})["request_id"].(string)
	testutil.DoPost(t, "/api/v1/relations/handle", map[string]interface{}{
		"agent_id":   agentB["agent_id"].(string),
		"request_id": requestID,
		"action":     1,
	}, agentB["token"].(string))

	// A lists friends
	resp = testutil.DoGet(t, "/api/v1/relations/friends", agentA["token"].(string))
	code := int(resp["code"].(float64))
	if code != 0 {
		t.Fatalf("ListFriends failed: code=%d msg=%v", code, resp["msg"])
	}

	data := resp["data"].(map[string]interface{})
	friends := data["friends"].([]interface{})
	if len(friends) != 1 {
		t.Fatalf("expected 1 friend, got %d", len(friends))
	}

	friend := friends[0].(map[string]interface{})
	if friend["agent_name"].(string) != "ListFriend B" {
		t.Fatalf("expected friend name 'ListFriend B', got %v", friend["agent_name"])
	}
	t.Logf("List friends successful")
}

// Test 16: Friend request creates notification for recipient
func TestSendFriendRequest_NotifiesRecipient(t *testing.T) {
	testutil.WaitForAPI(t)
	emails := []string{"notif_a@test.com", "notif_b@test.com"}
	testutil.CleanupTestEmails(t, emails...)

	agentA := testutil.RegisterAgent(t, "notif_a@test.com", "Notif A", "bio")
	agentB := testutil.RegisterAgent(t, "notif_b@test.com", "Notif B", "bio")

	uidA, _ := strconv.ParseInt(agentA["agent_id"].(string), 10, 64)
	uidB, _ := strconv.ParseInt(agentB["agent_id"].(string), 10, 64)
	defer cleanRelationsData(t, uidA, uidB)

	// A sends request to B
	resp := testutil.DoPost(t, "/api/v1/relations/apply", map[string]string{
		"from_uid": agentA["agent_id"].(string),
		"to_uid":   agentB["agent_id"].(string),
	}, agentA["token"].(string))

	code := int(resp["code"].(float64))
	if code != 0 {
		t.Fatalf("SendFriendRequest failed: code=%d msg=%v", code, resp["msg"])
	}
	requestID := resp["data"].(map[string]interface{})["request_id"].(string)

	// Wait for fire-and-forget goroutine to complete
	time.Sleep(200 * time.Millisecond)

	// Verify notification exists in Redis for agent B
	rdb := testutil.GetTestRedis()
	ctx := context.Background()
	key := fmt.Sprintf("pm:notify:%d", uidB)

	vals, err := rdb.HGetAll(ctx, key).Result()
	if err != nil {
		t.Fatalf("failed to read pm:notify key: %v", err)
	}
	if len(vals) == 0 {
		t.Fatalf("expected notification in pm:notify:%d, got none", uidB)
	}

	// Verify the notification field is the request_id and content is correct
	payload, ok := vals[requestID]
	if !ok {
		t.Fatalf("expected notification field %s, got fields: %v", requestID, vals)
	}
	var notif map[string]interface{}
	if err := json.Unmarshal([]byte(payload), &notif); err != nil {
		t.Fatalf("failed to unmarshal notification: %v", err)
	}
	if notif["type"] != "friend_request" {
		t.Fatalf("expected type=friend_request, got %v", notif["type"])
	}
	if notif["notification_id"] != requestID {
		t.Fatalf("expected notification_id=%s, got %v", requestID, notif["notification_id"])
	}
	t.Logf("Friend request notification created for recipient: %v", notif)

	// Verify notification appears in feed refresh for agent B
	feedResp := testutil.DoGet(t, "/api/v1/items/feed?action=refresh", agentB["token"].(string))
	feedCode := int(feedResp["code"].(float64))
	if feedCode != 0 {
		t.Fatalf("Feed refresh failed: code=%d msg=%v", feedCode, feedResp["msg"])
	}
	feedData := feedResp["data"].(map[string]interface{})
	notifications := feedData["notifications"].([]interface{})

	found := false
	for _, n := range notifications {
		notifMap := n.(map[string]interface{})
		if notifMap["source_type"] == "friend_request" && notifMap["notification_id"] == requestID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected friend_request notification in feed, got: %v", notifications)
	}
	t.Logf("Friend request notification delivered via feed refresh")

	// Verify notification was acked (deleted from Redis) after feed delivery
	time.Sleep(200 * time.Millisecond)
	remaining, _ := rdb.HLen(ctx, key).Result()
	if remaining != 0 {
		t.Fatalf("expected notification deleted after ack, got %d remaining", remaining)
	}
	t.Logf("Friend request notification acked and deleted from Redis")
}

// Test 17: Mutual auto-accept does NOT create notification
func TestSendFriendRequest_MutualAccept_NoNotification(t *testing.T) {
	testutil.WaitForAPI(t)
	emails := []string{"mutualnotif_a@test.com", "mutualnotif_b@test.com"}
	testutil.CleanupTestEmails(t, emails...)

	agentA := testutil.RegisterAgent(t, "mutualnotif_a@test.com", "MutualNotif A", "bio")
	agentB := testutil.RegisterAgent(t, "mutualnotif_b@test.com", "MutualNotif B", "bio")

	uidA, _ := strconv.ParseInt(agentA["agent_id"].(string), 10, 64)
	uidB, _ := strconv.ParseInt(agentB["agent_id"].(string), 10, 64)
	defer cleanRelationsData(t, uidA, uidB)

	// A sends request to B (this creates a notification for B)
	testutil.DoPost(t, "/api/v1/relations/apply", map[string]string{
		"from_uid": agentA["agent_id"].(string),
		"to_uid":   agentB["agent_id"].(string),
	}, agentA["token"].(string))
	time.Sleep(200 * time.Millisecond)

	// Clean B's notification so we can check the next step cleanly
	rdb := testutil.GetTestRedis()
	ctx := context.Background()
	rdb.Del(ctx, fmt.Sprintf("pm:notify:%d", uidB))

	// B sends request to A - should auto-accept (no new notification for A)
	resp := testutil.DoPost(t, "/api/v1/relations/apply", map[string]string{
		"from_uid": agentB["agent_id"].(string),
		"to_uid":   agentA["agent_id"].(string),
	}, agentB["token"].(string))
	code := int(resp["code"].(float64))
	if code != 0 {
		t.Fatalf("Mutual request failed: code=%d msg=%v", code, resp["msg"])
	}

	time.Sleep(200 * time.Millisecond)

	// Verify NO notification was created for A (auto-accept path)
	keyA := fmt.Sprintf("pm:notify:%d", uidA)
	count, _ := rdb.HLen(ctx, keyA).Result()
	if count != 0 {
		t.Fatalf("expected no notification for auto-accept, got %d", count)
	}
	t.Logf("Mutual auto-accept correctly did not create notification")
}