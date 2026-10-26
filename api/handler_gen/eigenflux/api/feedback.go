package api

import (
	"regexp"
	"strconv"
	"strings"

	apimodel "eigenflux_server/api/model/eigenflux/api"
	"eigenflux_server/pkg/itemstats"
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
	if author, known := authors[itemID]; known && author == agentID {
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
