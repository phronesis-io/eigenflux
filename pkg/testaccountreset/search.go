package testaccountreset

import (
	"encoding/json"
	"fmt"
	"strings"
)

// DeleteByQueryResult reads an Elasticsearch delete_by_query response.
//
// The items lifecycle policy makes every index read-only once it leaves the hot
// phase, so an account's older broadcasts cannot be deleted from search. Those
// write-block failures are counted as left, not treated as an error: the feed
// loads every search hit from PostgreSQL and drops hits whose row is gone. Any
// other failure, a timeout, or a version conflict is an error.
func DeleteByQueryResult(raw []byte) (deleted, leftReadOnly int64, err error) {
	var parsed struct {
		Deleted          int64 `json:"deleted"`
		TimedOut         bool  `json:"timed_out"`
		VersionConflicts int64 `json:"version_conflicts"`
		Failures         []struct {
			Index string `json:"index"`
			Cause struct {
				Type   string `json:"type"`
				Reason string `json:"reason"`
			} `json:"cause"`
		} `json:"failures"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return 0, 0, err
	}
	for _, f := range parsed.Failures {
		if f.Cause.Type != "cluster_block_exception" || !strings.Contains(f.Cause.Reason, "index write") {
			return parsed.Deleted, 0, fmt.Errorf("index %s: %s: %s", f.Index, f.Cause.Type, f.Cause.Reason)
		}
		leftReadOnly++
	}
	if parsed.TimedOut {
		return parsed.Deleted, leftReadOnly, fmt.Errorf("delete timed out after %d documents; rerun", parsed.Deleted)
	}
	if parsed.VersionConflicts > 0 {
		return parsed.Deleted, leftReadOnly, fmt.Errorf("%d version conflicts; rerun", parsed.VersionConflicts)
	}
	return parsed.Deleted, leftReadOnly, nil
}
