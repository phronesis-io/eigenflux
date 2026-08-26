package install

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestInviteLookupRateLimitIsSharedByIP(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	for attempt := 0; attempt < inviteLookupRateLimit; attempt++ {
		allowed, err := allowInviteLookup(context.Background(), client, "203.0.113.7")
		if err != nil || !allowed {
			t.Fatalf("attempt %d: allowed=%v err=%v", attempt+1, allowed, err)
		}
	}
	allowed, err := allowInviteLookup(context.Background(), client, "203.0.113.7")
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("expected the shared lookup limit to reject the next request")
	}

	otherAllowed, err := allowInviteLookup(context.Background(), client, "203.0.113.8")
	if err != nil || !otherAllowed {
		t.Fatalf("independent IP was limited: allowed=%v err=%v", otherAllowed, err)
	}
}
