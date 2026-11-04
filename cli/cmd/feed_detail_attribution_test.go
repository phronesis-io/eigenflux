package cmd

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/cache"
	"cli.eigenflux.ai/internal/feedevent"
)

func TestFeedGetCarriesOnlyHandedExposure(t *testing.T) {
	var received string
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		received = r.URL.Query().Get("impression_id")
		fmt.Fprint(w, `{"code":0,"data":{"item":{"item_id":"1","content":"body"}}}`)
	}))
	defer srv.Close()
	_, server := runtimeTestConfig(t, srv.URL, true)
	page := batchTestPage("handed", 2)
	if err := cache.SaveFeedResponse(server, page); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	if err := feedReportStore(server).Update(now, func(r *feedevent.Reports) {
		_, err := recordFeedPage(r, page, now, 1, false)
		if err != nil {
			t.Fatal(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = feedGetCmd.Flags().Set("item-id", "")
		_ = feedGetCmd.Flags().Set("impression-id", "")
	})
	for _, tc := range []struct{ item, explicit, want string }{
		{"1", "", "handed"}, {"2", "", ""}, {"3", "", ""}, {"1", "earlier", "earlier"},
	} {
		_ = feedGetCmd.Flags().Set("item-id", tc.item)
		_ = feedGetCmd.Flags().Set("impression-id", tc.explicit)
		if _, err := captureHeartbeatStdout(t, func() error { return feedGetCmd.RunE(feedGetCmd, nil) }); err != nil {
			t.Fatal(err)
		}
		if received != tc.want {
			t.Fatalf("item %s impression=%q want=%q", tc.item, received, tc.want)
		}
	}
	before := requests
	for _, invalid := range []string{strings.Repeat("x", 65), "imp\na"} {
		_ = feedGetCmd.Flags().Set("impression-id", invalid)
		if err := feedGetCmd.RunE(feedGetCmd, nil); err == nil {
			t.Fatal("invalid attribution accepted")
		}
	}
	if requests != before {
		t.Fatal("invalid attribution reached server")
	}
}
