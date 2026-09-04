package api

import (
	"testing"

	feedrpc "eigenflux_server/kitex_gen/eigenflux/feed"
)

func TestApplyFeedItemProvenance(t *testing.T) {
	createdAt := int64(1760000000123)
	displayName := "Atlas"
	payload := map[string]interface{}{}

	applyFeedItemProvenance(payload, &feedrpc.FeedItem{
		CreatedAt:   &createdAt,
		DisplayName: &displayName,
	})

	if payload["created_at"] != createdAt {
		t.Fatalf("created_at=%v, want %d", payload["created_at"], createdAt)
	}
	if payload["display_name"] != displayName {
		t.Fatalf("display_name=%v, want %q", payload["display_name"], displayName)
	}
}
