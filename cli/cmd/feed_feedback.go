package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/cache"
	"cli.eigenflux.ai/internal/client"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/feedevent"
	"cli.eigenflux.ai/internal/output"
	"github.com/spf13/cobra"
)

func feedReportStore(server string) *feedevent.ReportStore {
	return feedevent.NewReportStore(cache.ServerDataDir(server), feedReportScope(server))
}
func feedReportScope(server string) string {
	endpoint := server
	if cfg, err := config.Load(); err == nil {
		if srv, err := cfg.GetActive(server); err == nil {
			endpoint = strings.TrimRight(srv.Endpoint, "/")
		}
	}
	return endpoint + "\x00" + settingsAgentID(server)
}

func prepareFeedback(raw string, ledger *feedevent.Ledger, scope string, now int64) ([]feedevent.Event, error) {
	var items []feedevent.Event
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&items); err != nil {
		return nil, fmt.Errorf("invalid --items JSON: %w", err)
	}
	if len(items) == 0 || len(items) > feedevent.MaxBatch {
		return nil, fmt.Errorf("provide between 1 and %d feedback items", feedevent.MaxBatch)
	}
	for i, item := range items {
		id := coerceString(item["item_id"])
		if id == "" {
			return nil, fmt.Errorf("item %d: item_id is required", i)
		}
		scoreNumber, ok := item["score"].(json.Number)
		if !ok {
			return nil, fmt.Errorf("item %d: score must be an integer", i)
		}
		score, err := scoreNumber.Int64()
		if err != nil || score < -1 || score > 2 {
			return nil, fmt.Errorf("item %d: score must be -1, 0, 1 or 2", i)
		}
		var surfaced *bool
		if v, exists := item["surfaced"]; exists {
			b, ok := v.(bool)
			if !ok {
				return nil, fmt.Errorf("item %d: surfaced must be boolean", i)
			}
			surfaced = &b
		}
		imp := coerceString(item["impression_id"])
		delete(item, "impression_source")
		if imp != "" {
			item["impression_source"] = "explicit"
		} else if e, status := ledger.Lookup(id, now); status == feedevent.StatusHit && e.ImpressionID != "" {
			imp = e.ImpressionID
			item["impression_source"] = "cli_inferred"
		}
		item["item_id"] = id
		item["score"] = score
		if imp != "" {
			item["impression_id"] = imp
		} else {
			delete(item, "impression_id")
		}
		item["client_key"] = feedevent.FeedbackKey(scope, imp, id, int(score), surfaced)
		item["kind"] = "feedback"
		item["ts"] = now
		item["_scope"] = scope
	}
	return items, nil
}

func submitDurableFeedback(cmd *cobra.Command, _ []string) error {
	raw, _ := cmd.Flags().GetString("items")
	server, broadcasts, events := eventDirs()
	// Validate the saved account locally before deriving its scope. Network
	// credential refresh happens only after the judgment is durably queued.
	if _, err := queuedFeedClient(server); err != nil {
		return err
	}
	if settingsAgentID(server) == "" {
		return fmt.Errorf("authenticated Agent ID is unavailable for server %q", server)
	}
	now := time.Now().UnixMilli()
	items, err := prepareFeedback(raw, feedevent.NewLedger(broadcasts, server), feedReportScope(server), now)
	if err != nil {
		return err
	}
	q := feedevent.NewQueue(events)
	if err := q.Enqueue(items); err != nil {
		return err
	}
	warnReportLedger(feedReportStore(server).Update(now, func(r *feedevent.Reports) {
		for _, item := range items {
			r.RecordFeedback(item, now, "queued")
		}
	}))
	var response json.RawMessage
	foreground, err := authenticatedFeedClient(server)
	remaining := len(items)
	if err == nil {
		_, remaining, err = flushFeedQueueWithClient(server, foreground, func(data json.RawMessage) { response = data })
	}
	if err != nil || remaining > 0 {
		output.PrintMessage("Feedback queued for %d items; delivery will retry automatically", len(items))
	} else {
		output.PrintMessage("Feedback submitted for %d items", len(items))
	}
	if len(response) > 0 {
		output.PrintData(response, resolveFormat())
	} else {
		output.PrintData(map[string]any{"queued_count": len(items)}, resolveFormat())
	}
	return nil
}

// Background delivery uses only the saved access token: refresh/recovery belongs
// to the foreground authenticated command and cannot exceed this flush budget.
func queuedFeedClient(server string) (*client.Client, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	srv, err := cfg.GetActive(server)
	if err != nil {
		return nil, err
	}
	token, family := "", "/api/v1"
	hasV2, err := auth.HasV2Credentials(server)
	if err != nil {
		return nil, err
	}
	if hasV2 {
		creds, err := auth.LoadV2Credentials(server)
		if err != nil {
			return nil, err
		}
		token = creds.AccessToken
		family = "/api/v2"
	} else {
		creds, err := auth.LoadCredentials(server)
		if err != nil {
			return nil, err
		}
		token = creds.AccessToken
	}
	return client.New(strings.TrimRight(srv.Endpoint, "/")+family, token, version, clientMetaForServer(srv)), nil
}

type feedbackResult struct {
	ItemID string `json:"item_id"`
	Status string `json:"status"`
	Reason string `json:"reason"`
}

func flushFeedQueue(server string, onFeedback func(json.RawMessage)) (int, int, error) {
	return flushFeedQueueWithClient(server, nil, onFeedback)
}

func flushFeedQueueWithClient(server string, foreground *client.Client, onFeedback func(json.RawMessage)) (int, int, error) {
	deadline := time.Now().Add(1750 * time.Millisecond)
	q := feedevent.NewQueue(cache.ServerDataDir(server) + "/events")
	store := feedReportStore(server).WithLockWait(50 * time.Millisecond)
	now := time.Now().UnixMilli()
	return q.FlushWithDrops(now, func(batch []feedevent.Event) error {
		c := foreground
		if c == nil {
			var err error
			c, err = queuedFeedClient(server)
			if err != nil {
				return err
			}
		}
		var feedback, events []feedevent.Event
		for _, event := range batch {
			if scope, _ := event["_scope"].(string); scope != "" && scope != feedReportScope(server) {
				continue
			}
			if event["kind"] == "feedback" {
				feedback = append(feedback, event)
			} else {
				events = append(events, event)
			}
		}
		for _, group := range []struct {
			items    []feedevent.Event
			feedback bool
		}{{feedback, true}, {events, false}} {
			if len(group.items) == 0 {
				continue
			}
			remaining := time.Until(deadline)
			if remaining <= 0 {
				return fmt.Errorf("feed queue flush budget exhausted")
			}
			c.HTTPClient = &http.Client{Timeout: remaining}
			path, key := "/items/events", "events"
			body := map[string]any{}
			wire := make([]feedevent.Event, 0, len(group.items))
			for _, item := range group.items {
				copy := feedevent.Event{}
				for k, v := range item {
					if k != "_scope" && (!group.feedback || k != "kind" && k != "ts") {
						copy[k] = v
					}
				}
				wire = append(wire, copy)
			}
			if group.feedback {
				path, key = "/items/feedback", "items"
				body["contract_version"] = feedevent.FeedbackContractVersion
				warnReportLedger(store.Count(now, map[string]int64{"feedback_attempts": int64(len(wire))}))
			}
			body[key] = wire
			response, err := c.Post(path, body)
			if err == nil && response.Code != 0 {
				err = fmt.Errorf("%s", response.Msg)
			}
			if err != nil {
				if group.feedback {
					warnReportLedger(store.Count(now, map[string]int64{"feedback_failures": int64(len(wire))}))
				}
				return err
			}
			if group.feedback {
				var result struct {
					Results      []feedbackResult `json:"results"`
					SkippedCount int              `json:"skipped_count"`
				}
				if err := json.Unmarshal(response.Data, &result); err != nil {
					return err
				}
				// Older servers omit per-item results. A wholly accepted batch is safe;
				// ambiguous skips remain queued until a server can identify them.
				if len(result.Results) == 0 && result.SkippedCount == 0 {
					for _, item := range group.items {
						result.Results = append(result.Results, feedbackResult{ItemID: coerceString(item["item_id"]), Status: "accepted"})
					}
				}
				if len(result.Results) != len(group.items) {
					return fmt.Errorf("feedback response missing per-item results")
				}
				for i, res := range result.Results {
					if res.ItemID != coerceString(group.items[i]["item_id"]) || res.Status != "accepted" && res.Status != "duplicate" && res.Status != "rejected" {
						return fmt.Errorf("invalid feedback result")
					}
				}
				// The server has recorded the batch; a ledger failure must not keep it
				// queued for resending.
				warnReportLedger(store.Update(now, func(r *feedevent.Reports) {
					for i, res := range result.Results {
						r.RecordFeedback(group.items[i], now, res.Status)
					}
				}))
				if onFeedback != nil {
					onFeedback(response.Data)
					for _, res := range result.Results {
						if res.Status == "rejected" {
							output.PrintMessage("Feedback rejected for %s: %s", res.ItemID, res.Reason)
						}
					}
				}
			}
		}
		return nil
	}, func(count int) error {
		warnReportLedger(store.Count(now, map[string]int64{"queue_drops": int64(count)}))
		return nil
	})
}

func maybeFlushFeedQueue() {
	server := activeServerName()
	if server == "" {
		return
	}
	_, _, _ = flushFeedQueue(server, nil)
}
