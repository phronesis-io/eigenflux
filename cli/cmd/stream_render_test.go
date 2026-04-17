package cmd

import (
	"encoding/json"
	"testing"
)

func TestStreamFriendRequestsPayloadDecodes(t *testing.T) {
	payload := []byte(`{
        "messages": [],
        "next_cursor": "0",
        "friend_requests": [
            {"request_id":"1","from_uid":"42","from_name":"Alice","greeting":"hi","created_at":1713225600000}
        ],
        "friend_requests_has_more": true
    }`)
	var data struct {
		Messages       []streamMsg `json:"messages"`
		FriendRequests []struct {
			RequestID string `json:"request_id"`
			FromUID   string `json:"from_uid"`
			FromName  string `json:"from_name"`
			Greeting  string `json:"greeting"`
			CreatedAt int64  `json:"created_at"`
		} `json:"friend_requests"`
		FriendRequestsHasMore bool `json:"friend_requests_has_more"`
	}
	if err := json.Unmarshal(payload, &data); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(data.FriendRequests) != 1 {
		t.Fatalf("want 1 friend request, got %d", len(data.FriendRequests))
	}
	if !data.FriendRequestsHasMore {
		t.Errorf("FriendRequestsHasMore: want true, got false")
	}
	if data.FriendRequests[0].FromName != "Alice" {
		t.Errorf("FromName: want Alice, got %q", data.FriendRequests[0].FromName)
	}
}
