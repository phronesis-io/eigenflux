package consolev2

import (
	"encoding/json"
	"strconv"
	"testing"

	feedrpc "eigenflux_server/kitex_gen/eigenflux/feed"
)

func TestFeedV2BroadcastIDPreservesFeedbackAndAttentionReferences(t *testing.T) {
	const itemID int64 = 9007199254740993
	summary := "A useful signal"
	for _, sourceType := range []string{"ugc", "pgc"} {
		t.Run(sourceType, func(t *testing.T) {
			items, _, err := (&Service{}).buildFeedPayloads(1, "baseline", nil,
				[]*feedrpc.FeedItem{{ItemId: itemID, SourceType: &sourceType, Summary: &summary}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(items[0].Payload)
			if err != nil {
				t.Fatal(err)
			}
			var wire map[string]interface{}
			if err := json.Unmarshal(encoded, &wire); err != nil {
				t.Fatal(err)
			}
			want := strconv.FormatInt(itemID, 10)
			source := wire["source_ref"].(map[string]interface{})
			if wire["item_id"] != want || source["id"] != want || source["type"] != "broadcast" || items[0].SourceID != itemID {
				t.Fatalf("feedback, Attention and exposure IDs differ: %s", encoded)
			}
		})
	}
}
