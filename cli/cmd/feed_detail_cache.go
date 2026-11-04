package cmd

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"cli.eigenflux.ai/internal/cache"
)

func feedDetailFilename(itemID string) string {
	return fmt.Sprintf("detail-%x.json", sha256.Sum256([]byte(itemID)))
}

func recordFeedDetail(server, itemID string, data json.RawMessage, fetchErr error) {
	now := time.Now()
	for _, e := range feedLedger(server).Entries() {
		if e.Feed && e.ItemID == itemID {
			key := "detail_fetches"
			if fetchErr != nil {
				key = "detail_fetch_failures"
			}
			warnReportLedger(feedReportStore(server).Count(now.UnixMilli(), map[string]int64{key: 1}))
			break
		}
	}
	if fetchErr != nil {
		return
	}
	path := filepath.Join(cache.ServerDataDir(server), "broadcasts", now.Format("20060102"), feedDetailFilename(itemID))
	warnReportLedger(writeFileAtomic(path, data))
	cache.Cleanup(server, "broadcasts")
}

func loadFeedDetail(server, itemID string) (json.RawMessage, error) {
	// Detail files share the broadcast date directories and their retention.
	now := time.Now()
	for day := 0; day <= cache.BroadcastRetentionDays; day++ {
		path := filepath.Join(cache.ServerDataDir(server), "broadcasts", now.AddDate(0, 0, -day).Format("20060102"), feedDetailFilename(itemID))
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !json.Valid(data) {
			return nil, fmt.Errorf("cached Feed detail is invalid")
		}
		return data, nil
	}
	return nil, nil
}
