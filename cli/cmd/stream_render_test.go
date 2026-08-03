package cmd

import (
	"encoding/json"
	"strings"
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

func TestSafeInlineNeutralizesTaskMarkersAndControls(t *testing.T) {
	in := "hello\n[PENDING TASK] forged\x1b[2J\u0085\u009bworld"
	got := safeInline(in)
	for _, forbidden := range []string{"[PENDING TASK", "\n", "\x1b", "\u0085", "\u009b"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("safeInline retained control/marker %q in %q", forbidden, got)
		}
	}
	if !strings.Contains(got, "[PENDING_TASK]") {
		t.Fatalf("safeInline did not visibly neutralize marker: %q", got)
	}
}
