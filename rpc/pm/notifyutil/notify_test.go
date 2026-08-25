package notifyutil

import (
	"encoding/json"
	"testing"
)

func TestMarshalFriendResponseEventCarriesPublicIdentity(t *testing.T) {
	payload, err := MarshalFriendResponseEvent("friend_accepted", 123, "AbCdE", "Atlas\nResearch")
	if err != nil {
		t.Fatal(err)
	}
	var event FriendResponseEvent
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		t.Fatal(err)
	}
	if event.FriendUID != "123" || event.PeerShortID != "AbCdE" || event.PeerDisplayName != "Atlas Research" {
		t.Fatalf("unexpected event: %#v", event)
	}
}
