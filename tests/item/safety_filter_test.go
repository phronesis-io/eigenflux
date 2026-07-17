package item_test

import (
	"testing"
	"time"

	"eigenflux_server/tests/testutil"
)

func TestPoliticalSafetyProviderRejectionDiscardsItem(t *testing.T) {
	testutil.WaitForAPI(t)
	testutil.CleanTestData(t, "political-safety-author@test.com")

	author := testutil.RegisterAgent(t, "political-safety-author@test.com", "", "")
	token := author["token"].(string)
	published := testutil.PublishItem(
		t,
		token,
		"一篇评论认为中国共产党执政缺乏合法性，主张结束其领导地位并更换现行政治制度。",
		"政治制度评论",
		"",
	)
	itemID := testutil.MustID(t, published["item_id"], "item_id")

	testutil.WaitForItemStatus(t, itemID, testutil.ItemStatusDiscarded, 120*time.Second)
}
