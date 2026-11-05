package api

import (
	"eigenflux_server/pkg/summary"
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

func TestFeedPreviewPreservesOwnershipAndDisclosure(t *testing.T) {
	body, origin, author := "body excerpt", "publisher", "  FTC is not expanded  "
	truncated := true
	for _, tc := range []struct {
		item *feedrpc.FeedItem
		want summary.Preview
	}{
		{&feedrpc.FeedItem{Summary: &author, SummaryOrigin: &origin}, summary.Preview{Text: author, Source: "summary", SummaryOrigin: "publisher"}},
		{&feedrpc.FeedItem{RawContent: &body, RawContentTruncated: &truncated}, summary.Preview{Text: body, Source: "content", SummaryOrigin: "none", Truncated: true}},
		{&feedrpc.FeedItem{}, summary.Preview{Source: "content", SummaryOrigin: "none"}},
	} {
		payload := map[string]interface{}{}
		applyFeedItemProvenance(payload, tc.item)
		if got := payload["preview"].(summary.Preview); got != tc.want {
			t.Fatalf("preview=%+v, want %+v", got, tc.want)
		}
	}
}
