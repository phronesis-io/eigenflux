package dal

import (
	"strings"
	"testing"
)

func TestBroadcastQueriesLimitBeforeJoiningRawContent(t *testing.T) {
	for name, query := range map[string]string{
		"top":      top7DayBroadcastsQuery,
		"new_user": newUserBroadcastsQuery,
	} {
		t.Run(name, func(t *testing.T) {
			limitAt := strings.Index(query, "LIMIT ?")
			rawJoinAt := strings.Index(query, "JOIN raw_items")
			if limitAt < 0 || rawJoinAt < 0 || limitAt > rawJoinAt {
				t.Fatalf("query must limit ranked metadata before joining raw_items")
			}
			if strings.Contains(query[:limitAt], "raw_content") {
				t.Fatalf("ranked CTE must not read raw_content before LIMIT")
			}
		})
	}
}
