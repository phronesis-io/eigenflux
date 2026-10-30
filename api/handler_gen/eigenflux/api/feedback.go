package api

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"

	apimodel "eigenflux_server/api/model/eigenflux/api"
	"eigenflux_server/pkg/itemstats"
	"eigenflux_server/pkg/logger"
	"github.com/redis/go-redis/v9"
)

var feedbackClientKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)
var feedbackContractPattern = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,32}$`)

func normalizeFeedbackContract(value *string) (string, bool) {
	if value == nil {
		return "", true
	}
	version := strings.TrimSpace(*value)
	return version, feedbackContractPattern.MatchString(version)
}

func normalizeFeedbackItem(agentID int64, item *apimodel.FeedbackItem, batchImpression, contract string, authors map[int64]int64) (itemstats.Feedback, string) {
	if item == nil {
		return itemstats.Feedback{}, "invalid item_id "
	}
	itemID, err := strconv.ParseInt(item.ItemID, 10, 64)
	if err != nil {
		return itemstats.Feedback{}, "invalid item_id " + item.ItemID
	}
	if item.Score < -1 || item.Score > 2 {
		return itemstats.Feedback{}, "invalid score for item " + item.ItemID
	}
	author, known := authors[itemID]
	if !known {
		// Deleted broadcasts keep their raw_items row, so an absent ID was
		// never published (often a JSON-number ID rounded by the client).
		return itemstats.Feedback{}, "unknown item " + item.ItemID
	}
	if author == agentID {
		return itemstats.Feedback{}, "own item " + item.ItemID
	}
	feedback := itemstats.Feedback{AgentID: agentID, ItemID: itemID, Score: int(item.Score), ImpressionID: strings.TrimSpace(batchImpression), ContractVersion: contract, Surfaced: item.Surfaced}
	if item.ImpressionID != nil && strings.TrimSpace(*item.ImpressionID) != "" {
		feedback.ImpressionID = strings.TrimSpace(*item.ImpressionID)
	}
	if item.ClientKey != nil {
		feedback.ClientKey = strings.TrimSpace(*item.ClientKey)
		if !feedbackClientKeyPattern.MatchString(feedback.ClientKey) {
			return itemstats.Feedback{}, "invalid client_key for item " + item.ItemID
		}
	}
	if item.ImpressionSource != nil {
		feedback.ImpressionSource = strings.TrimSpace(*item.ImpressionSource)
		if feedback.ImpressionID == "" || (feedback.ImpressionSource != "explicit" && feedback.ImpressionSource != "cli_inferred") {
			return itemstats.Feedback{}, "invalid impression_source for item " + item.ItemID
		}
	}
	return feedback, ""
}

// feedbackActivityClaimTTL covers the CLI's 72-hour report queue retention.
const feedbackActivityClaimTTL = 96 * time.Hour

type acceptedFeedback struct {
	clientKey string
	score     int
}

func feedbackActivityClaimKey(agentID int64, clientKey string) string {
	return "feedback:activity:" + strconv.FormatInt(agentID, 10) + ":" + clientKey
}

// countFeedbackActivity counts accepted items for Agent activity. A keyed item
// counts only on its first accepted report, so client retries do not repeat
// activity or worth-reading counts; item stats are deduplicated by the consumer.
// Without Redis every item counts, as unkeyed items always do.
func countFeedbackActivity(ctx context.Context, rdb *redis.Client, agentID int64, items []acceptedFeedback) (count, useful, kept int) {
	counted := make([]bool, len(items))
	claims := make(map[int]*redis.BoolCmd)
	var pipe redis.Pipeliner
	for i, item := range items {
		counted[i] = true
		if item.clientKey == "" || rdb == nil {
			continue
		}
		if pipe == nil {
			pipe = rdb.Pipeline()
		}
		claims[i] = pipe.SetNX(ctx, feedbackActivityClaimKey(agentID, item.clientKey), 1, feedbackActivityClaimTTL)
	}
	if pipe != nil {
		if _, err := pipe.Exec(ctx); err != nil {
			logger.Ctx(ctx).Warn("BatchFeedback activity claim failed", "agentID", agentID, "err", err)
		} else {
			for i, claim := range claims {
				counted[i] = claim.Val()
			}
		}
	}
	for i, item := range items {
		if !counted[i] {
			continue
		}
		count++
		if item.score == 2 {
			useful++
		}
		if item.score >= 1 {
			kept++
		}
	}
	return count, useful, kept
}
