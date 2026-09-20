package testutil

import (
	"context"
	"fmt"
	"testing"
	"time"

	"eigenflux_server/pkg/milestone"
)

func ConfigureMilestoneRuleForTest(t *testing.T, metricKey string, threshold int64, contentTemplate string) {
	t.Helper()

	now := time.Now().UnixMilli()
	if contentTemplate == "" {
		contentTemplate = `Your Content "{{.ItemSummary}}" reached {{.CounterValue}} ` + metricKey + ` milestones. Item Id {{.ItemID}}`
	}

	if _, err := TestDB.Exec(
		"UPDATE milestone_rules SET rule_enabled = FALSE, updated_at = $1 WHERE metric_key = $2",
		now, metricKey,
	); err != nil {
		t.Fatalf("disable milestone rules failed for metric %s: %v", metricKey, err)
	}

	if _, err := TestDB.Exec(`
		INSERT INTO milestone_rules (metric_key, threshold, rule_enabled, content_template, created_at, updated_at)
		VALUES ($1, $2, TRUE, $3, $4, $4)
		ON CONFLICT (metric_key, threshold)
		DO UPDATE SET
			rule_enabled = EXCLUDED.rule_enabled,
			content_template = EXCLUDED.content_template,
			updated_at = EXCLUDED.updated_at
	`, metricKey, threshold, contentTemplate, now); err != nil {
		t.Fatalf("upsert milestone rule failed for metric %s threshold %d: %v", metricKey, threshold, err)
	}
	if err := milestone.PublishRuleInvalidation(context.Background(), GetTestRedis(), metricKey); err != nil {
		t.Fatalf("invalidate milestone rule cache for metric %s: %v", metricKey, err)
	}
}

func WaitForMilestoneEvents(t *testing.T, authorAgentID int64, expectedCount int, timeout time.Duration) []map[string]interface{} {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		rows, err := TestDB.Query(`
			SELECT event_id, metric_key, threshold, counter_value, notification_status, notification_content
			FROM milestone_events
			WHERE author_agent_id = $1
			ORDER BY threshold ASC, event_id ASC
		`, authorAgentID)
		if err != nil {
			t.Fatalf("query milestone events failed: %v", err)
		}

		var events []map[string]interface{}
		for rows.Next() {
			var eventID, threshold, counterValue int64
			var metricKey, content string
			var status int
			if scanErr := rows.Scan(&eventID, &metricKey, &threshold, &counterValue, &status, &content); scanErr != nil {
				rows.Close()
				t.Fatalf("scan milestone event failed: %v", scanErr)
			}
			events = append(events, map[string]interface{}{
				"event_id":             eventID,
				"metric_key":           metricKey,
				"threshold":            threshold,
				"counter_value":        counterValue,
				"notification_status":  status,
				"notification_content": content,
			})
		}
		rows.Close()

		if len(events) >= expectedCount {
			return events
		}
		time.Sleep(500 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for %d milestone events for author %d", expectedCount, authorAgentID)
	return nil
}

func WaitForFeedNotifications(t *testing.T, token string, expectedMin int, timeout time.Duration) map[string]interface{} {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		feed := FetchFeedRefresh(t, token, 20)
		notifications, ok := feed["notifications"].([]interface{})
		if ok && len(notifications) >= expectedMin {
			return feed
		}
		time.Sleep(500 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for at least %d notifications in feed", expectedMin)
	return nil
}

func WaitForMilestoneStatuses(t *testing.T, eventIDs []int64, expectedStatus int, timeout time.Duration) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ok := true
		for _, eventID := range eventIDs {
			var status int
			err := TestDB.QueryRow(
				"SELECT notification_status FROM milestone_events WHERE event_id = $1",
				eventID,
			).Scan(&status)
			if err != nil || status != expectedStatus {
				ok = false
				break
			}
		}
		if ok {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}

	t.Fatalf("timed out waiting for milestone events %v to reach status %d", eventIDs, expectedStatus)
}

func DumpMilestoneEvents(t *testing.T, authorAgentID int64) {
	t.Helper()

	rows, err := TestDB.Query(`
		SELECT event_id, metric_key, threshold, counter_value, notification_status, notification_content
		FROM milestone_events
		WHERE author_agent_id = $1
		ORDER BY threshold ASC, event_id ASC
	`, authorAgentID)
	if err != nil {
		t.Logf("dump milestone events query failed: %v", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var eventID, threshold, counterValue int64
		var metricKey, content string
		var status int
		if scanErr := rows.Scan(&eventID, &metricKey, &threshold, &counterValue, &status, &content); scanErr != nil {
			t.Logf("dump milestone event scan failed: %v", scanErr)
			return
		}
		t.Logf("milestone event: id=%d metric=%s threshold=%d count=%d status=%d content=%s",
			eventID, metricKey, threshold, counterValue, status, content)
	}
}

func AssertNotificationIDs(t *testing.T, notifications []interface{}) []int64 {
	t.Helper()

	ids := make([]int64, 0, len(notifications))
	for _, raw := range notifications {
		notification := raw.(map[string]interface{})
		id := MustID(t, notification["notification_id"], "notification_id")
		ids = append(ids, id)
	}
	return ids
}

func NotificationContents(notifications []interface{}) []string {
	contents := make([]string, 0, len(notifications))
	for _, raw := range notifications {
		notification := raw.(map[string]interface{})
		content, _ := notification["content"].(string)
		contents = append(contents, content)
	}
	return contents
}

func PrintNotifications(t *testing.T, notifications []interface{}) {
	t.Helper()
	for idx, raw := range notifications {
		notification := raw.(map[string]interface{})
		t.Logf("notification[%d]: id=%v type=%v content=%v created_at=%v",
			idx,
			notification["notification_id"],
			notification["type"],
			notification["content"],
			notification["created_at"],
		)
	}
}

func MilestoneRuleTemplate(metricKey string) string {
	switch metricKey {
	case "consumed":
		return `Your Content "{{.ItemSummary}}" reached {{.CounterValue}} consumptions. Item Id {{.ItemID}}`
	case "score_1":
		return `Your Content "{{.ItemSummary}}" reached {{.CounterValue}} score_1 ratings. Item Id {{.ItemID}}`
	case "score_2":
		return `Your Content "{{.ItemSummary}}" reached {{.CounterValue}} score_2 ratings. Item Id {{.ItemID}}`
	default:
		return fmt.Sprintf(`Your Content "{{.ItemSummary}}" reached {{.CounterValue}} %s. Item Id {{.ItemID}}`, metricKey)
	}
}
