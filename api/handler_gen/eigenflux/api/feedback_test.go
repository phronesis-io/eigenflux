package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	apimodel "eigenflux_server/api/model/eigenflux/api"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestNormalizeFeedbackItem(t *testing.T) {
	for _, tc := range []struct{ name, body, batch, reason string }{
		{"legacy", `{"item_id":"10","score":0}`, "", ""},
		{"metadata", `{"item_id":"10","score":2,"client_key":" report:1._- ","surfaced":false,"impression_source":" cli_inferred "}`, " batch ", ""},
		{"override", `{"item_id":"10","score":-1,"impression_id":" item ","impression_source":"explicit"}`, "batch", ""},
		{"id", `{"item_id":"invalid","score":0}`, "", "invalid item_id invalid"},
		{"score", `{"item_id":"10","score":3}`, "", "invalid score for item 10"},
		{"own", `{"item_id":"11","score":1}`, "", "own item 11"},
		// A JSON-number ID rounded to float64 precision names no broadcast.
		{"unknown", `{"item_id":"373718292642136060","score":1}`, "", "unknown item 373718292642136060"},
		{"empty_key", `{"item_id":"10","score":0,"client_key":" "}`, "", "invalid client_key for item 10"},
		{"invalid_key", `{"item_id":"10","score":0,"client_key":"a/b"}`, "", "invalid client_key for item 10"},
		{"max_key", `{"item_id":"10","score":1,"client_key":"` + strings.Repeat("a", 64) + `","surfaced":true}`, "", ""},
		{"long_key", `{"item_id":"10","score":0,"client_key":"` + strings.Repeat("a", 65) + `"}`, "", "invalid client_key for item 10"},
		{"missing_impression", `{"item_id":"10","score":0,"impression_source":"explicit"}`, "", "invalid impression_source for item 10"},
		{"invalid_source", `{"item_id":"10","score":0,"impression_source":"inferred"}`, "batch", "invalid impression_source for item 10"},
		{"empty_source", `{"item_id":"10","score":0,"impression_source":" "}`, "batch", "invalid impression_source for item 10"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var item apimodel.FeedbackItem
			require.NoError(t, json.Unmarshal([]byte(tc.body), &item))
			got, reason := normalizeFeedbackItem(1, &item, tc.batch, "v2", map[int64]int64{10: 2, 11: 1})
			require.Equal(t, tc.reason, reason)
			if reason != "" {
				return
			}
			require.Equal(t, int64(10), got.ItemID)
			require.Equal(t, "v2", got.ContractVersion)
			switch tc.name {
			case "legacy":
				require.Nil(t, got.Surfaced)
				require.Empty(t, got.ClientKey)
				require.Empty(t, got.ImpressionID)
			case "metadata":
				require.NotNil(t, got.Surfaced)
				require.False(t, *got.Surfaced)
				require.Equal(t, "report:1._-", got.ClientKey)
				require.Equal(t, "batch", got.ImpressionID)
				require.Equal(t, "cli_inferred", got.ImpressionSource)
			case "override":
				require.Equal(t, "item", got.ImpressionID)
			case "max_key":
				require.Equal(t, strings.Repeat("a", 64), got.ClientKey)
				require.NotNil(t, got.Surfaced)
				require.True(t, *got.Surfaced)
			}
		})
	}
}

func TestNormalizeFeedbackContract(t *testing.T) {
	value, ok := normalizeFeedbackContract(nil)
	require.True(t, ok)
	require.Empty(t, value)
	for _, tc := range []struct {
		value string
		valid bool
	}{{" feedback/v2.0_- ", true}, {strings.Repeat("v", 32), true}, {"", false}, {" ", false}, {"v2:beta", false}, {strings.Repeat("v", 33), false}, {"版本", false}} {
		value, ok := normalizeFeedbackContract(&tc.value)
		require.Equal(t, tc.valid, ok, tc.value)
		if ok {
			require.Equal(t, strings.TrimSpace(tc.value), value)
		}
	}
}

func TestCountFeedbackActivityCountsEachClientKeyOnce(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { rdb.Close() })
	ctx := context.Background()
	batch := []acceptedFeedback{{"", 2}, {"a", 2}, {"b", 1}, {"a", 0}, {"c", -1}}
	count, useful, kept := countFeedbackActivity(ctx, rdb, 7, batch)
	require.Equal(t, []int{4, 2, 3}, []int{count, useful, kept})
	// A retry counts only its unkeyed items; another Agent's keys are separate.
	count, useful, kept = countFeedbackActivity(ctx, rdb, 7, batch)
	require.Equal(t, []int{1, 1, 1}, []int{count, useful, kept})
	count, _, _ = countFeedbackActivity(ctx, rdb, 8, batch)
	require.Equal(t, 4, count)
	ttl := mr.TTL(feedbackActivityClaimKey(7, "a"))
	require.Equal(t, feedbackActivityClaimTTL, ttl)
	// Without a usable Redis every accepted item counts.
	count, _, _ = countFeedbackActivity(ctx, nil, 7, batch)
	require.Equal(t, 5, count)
	mr.Close()
	count, useful, kept = countFeedbackActivity(ctx, rdb, 7, batch)
	require.Equal(t, []int{5, 2, 3}, []int{count, useful, kept})
}
