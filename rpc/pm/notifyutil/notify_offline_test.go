package notifyutil_test

import (
	"context"
	"testing"

	"eigenflux_server/rpc/notification/dal"
	"eigenflux_server/rpc/pm/notifyutil"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestOfflineFriendResponsePreservesPublicIdentity(t *testing.T) {
	for index, eventType := range []string{"friend_accepted", "friend_rejected"} {
		t.Run(eventType, func(t *testing.T) {
			server, err := miniredis.Run()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(server.Close)
			client := redis.NewClient(&redis.Options{Addr: server.Addr()})
			t.Cleanup(func() { _ = client.Close() })

			ctx := context.Background()
			if err := notifyutil.WriteFriendResponseNotification(ctx, client, int64(19+index), 42, 84, "AbCdE", "Atlas\nResearch", eventType, ""); err != nil {
				t.Fatal(err)
			}
			notifications, err := dal.ListPMNotifications(ctx, client, 42)
			if err != nil {
				t.Fatal(err)
			}
			if len(notifications) != 1 {
				t.Fatalf("got %d notifications, want 1", len(notifications))
			}
			got := notifications[0]
			if got.Type != eventType || got.FriendUID != 84 || got.PeerShortID != "AbCdE" || got.PeerDisplayName != "Atlas Research" {
				t.Fatalf("unexpected notification: %#v", got)
			}
		})
	}
}
