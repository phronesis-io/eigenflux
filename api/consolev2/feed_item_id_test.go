package consolev2

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	feedrpc "eigenflux_server/kitex_gen/eigenflux/feed"
)

func TestFeedPreviewSourceAndTruncation(t *testing.T) {
	short := "summary differs from original"
	long := strings.Repeat("原", 900)
	empty := ""
	for _, tc := range []struct {
		summary   *string
		content   string
		source    string
		truncated bool
	}{
		{&short, long, "summary", false}, {nil, short, "content", false}, {nil, long, "content", true}, {&long, short, "summary", true}, {&empty, short, "content", false},
	} {
		items, _, err := (&Service{}).buildFeedPayloads(1, "baseline", nil, []*feedrpc.FeedItem{{ItemId: 1, Summary: tc.summary, RawContent: &tc.content}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(items[0].Payload)
		var wire struct {
			Preview struct {
				Source    string `json:"source"`
				Text      string `json:"text"`
				Truncated bool   `json:"truncated"`
			} `json:"preview"`
		}
		if err := json.Unmarshal(raw, &wire); err != nil {
			t.Fatal(err)
		}
		if wire.Preview.Source != tc.source || wire.Preview.Truncated != tc.truncated || len([]rune(wire.Preview.Text)) > 800 {
			t.Fatal(wire)
		}
	}
}

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
