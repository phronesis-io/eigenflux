package notifyutil

import (
	"encoding/json"
	"testing"
)

func TestMarshalFriendResponseEventCarriesPublicIdentity(t *testing.T) {
	for _, eventType := range []string{"friend_accepted", "friend_rejected"} {
		t.Run(eventType, func(t *testing.T) {
			payload, err := MarshalFriendResponseEvent(eventType, 123, "AbCdE", "Atlas\nResearch")
			if err != nil {
				t.Fatal(err)
			}
			var event FriendResponseEvent
			if err := json.Unmarshal([]byte(payload), &event); err != nil {
				t.Fatal(err)
			}
			if event.Type != eventType || event.FriendUID != "123" || event.PeerShortID != "AbCdE" || event.PeerDisplayName != "Atlas Research" {
				t.Fatalf("unexpected event: %#v", event)
			}
		})
	}
}
