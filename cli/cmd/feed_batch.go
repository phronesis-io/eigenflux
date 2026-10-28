package cmd

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/cache"
	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/controlcontext"
	"cli.eigenflux.ai/internal/feedevent"
	"cli.eigenflux.ai/internal/output"
	"github.com/spf13/cobra"
)

const (
	feedBatchMax              = 20
	feedBacklogAge            = 24 * time.Hour
	feedRehandLimit           = 3
	feedLocalPollLimit        = 2
	feedFetchInterval         = 30 * time.Minute
	feedSurfaceReminderWindow = 2 * time.Hour
)

type localFeedBatch struct {
	Source       string `json:"source"`
	ImpressionID string `json:"impression_id"`
	Handed       int    `json:"handed"`
	Open         int    `json:"open"`
	Backlog      int    `json:"backlog"`
	NextCommand  string `json:"next_command"`
}

func feedLedger(server string) *feedevent.Ledger {
	return feedevent.NewLedger(filepath.Join(cache.ServerDataDir(server), "broadcasts"), server)
}

func feedBatchSize(cmd *cobra.Command) (int, error) {
	n, _ := cmd.Flags().GetInt("batch")
	if n < 1 || n > feedBatchMax {
		return 0, fmt.Errorf("--batch must be between 1 and %d", feedBatchMax)
	}
	return n, nil
}

func batchCandidates(ledger *feedevent.Ledger, state feedevent.Reports, now int64) (open, backlog []feedevent.Entry) {
	for _, e := range ledger.Entries() {
		r := state.Exposure(e)
		if !e.Feed || r.Baseline || r.ServedAt <= now-feedBacklogAge.Milliseconds() || r.ServedAt > now {
			continue
		}
		if r.HandedTime() == 0 {
			backlog = append(backlog, e)
		} else if r.ScoredAt == 0 && r.Delivery == "" && !state.Surfaced(e.ItemID) && r.Hands() < feedRehandLimit {
			open = append(open, e)
		}
	}
	for _, entries := range [][]feedevent.Entry{open, backlog} {
		sort.Slice(entries, func(i, j int) bool {
			a, b := state.Exposure(entries[i]), state.Exposure(entries[j])
			if a.ServedAt != b.ServedAt {
				return a.ServedAt < b.ServedAt
			}
			if a.ImpressionID != b.ImpressionID {
				return a.ImpressionID < b.ImpressionID
			}
			return a.Position < b.Position
		})
	}
	return open, backlog
}

// Owner checks always fetch fresh content; saved work follows through feed next.
func shouldFetchFeedBatch(action, origin string, state feedevent.Reports, open, backlog int, now int64) bool {
	return action == "more" || origin == publishOriginOwner || open+backlog == 0 ||
		(state.LocalPolls >= feedLocalPollLimit && now-state.LastFetchAt >= feedFetchInterval.Milliseconds())
}

func expireFeedBacklog(ledger *feedevent.Ledger, state *feedevent.Reports, now int64) {
	var expired int64
	for _, e := range ledger.Entries() {
		r := state.Exposure(e)
		if e.Feed && !r.Baseline && r.HandedTime() == 0 && r.BacklogExpiredAt == 0 && r.ServedAt <= now-feedBacklogAge.Milliseconds() {
			r.BacklogExpiredAt = now
			state.Entries[r.ImpressionID+"\x00"+r.ItemID] = r
			expired++
		}
	}
	if expired > 0 {
		state.Count(now, map[string]int64{"backlog_expired_items": expired})
	}
}

func batchNextCommand(server string, n int) string {
	prefix := "eigenflux --homedir " + shellQuote(config.HomeDir()) + " --server " + shellQuote(server)
	if clientMeta.Mode != "" {
		prefix += " --runtime-mode " + shellQuote(clientMeta.Mode)
	}
	if clientMeta.Model != "" {
		prefix += " --runtime-model " + shellQuote(clientMeta.Model)
	}
	if originFlag != "" {
		prefix += " --origin " + shellQuote(originFlag)
	}
	return fmt.Sprintf("%s feed next --batch %d", prefix, n)
}

func makeFeedBatch(server string, payload json.RawMessage, selected []feedevent.Entry, state feedevent.Reports, meta localFeedBatch) (json.RawMessage, error) {
	var page map[string]json.RawMessage
	if err := json.Unmarshal(payload, &page); err != nil {
		return nil, err
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(page["items"], &items); err != nil {
		return nil, err
	}
	batch := make([]map[string]json.RawMessage, 0, len(selected))
	for _, e := range selected {
		if e.Position >= len(items) {
			return nil, fmt.Errorf("cached Feed position is unavailable")
		}
		item := items[e.Position]
		var id string
		if json.Unmarshal(item["item_id"], &id) != nil || id != e.ItemID {
			return nil, fmt.Errorf("cached Feed item does not match its exposure")
		}
		item["already_surfaced"], _ = json.Marshal(state.Surfaced(e.ItemID))
		batch = append(batch, item)
	}
	page["items"], _ = json.Marshal(batch)
	page["local_batch"], _ = json.Marshal(meta)
	if meta.Source == "local" {
		page["notifications"] = json.RawMessage(`[]`)
		cached, err := controlcontext.Load(server, settingsAgentID(server))
		if err != nil {
			return nil, err
		}
		page["control_context_snapshot"] = cached.Context
		page["control_context_source"] = json.RawMessage(`"local_applied_cache"`)
		var personalization map[string]json.RawMessage
		if err := json.Unmarshal(page["personalization"], &personalization); err != nil {
			return nil, err
		}
		if personalization == nil {
			return nil, fmt.Errorf("cached Feed personalization is unavailable")
		}
		personalization["context_revision"], _ = json.Marshal(cached.Revision)
		page["personalization"], _ = json.Marshal(personalization)
	}
	contract, err := output.ResolveFeedContract(payload)
	if err != nil {
		return nil, err
	}
	page["output_contract"], _ = json.Marshal(contract)
	return json.Marshal(page)
}

// Select and persist under the existing report lock. Revalidate the invocation's
// snapshot so a competing caller cannot turn newly handed items into its retries.
func localFeedBatchPayload(server string, ledger *feedevent.Ledger, n int, poll bool) (json.RawMessage, *localFeedBatch, error) {
	now := time.Now().UnixMilli()
	store := feedReportStore(server)
	before, err := store.Read()
	if err != nil {
		return nil, nil, err
	}
	var payload json.RawMessage
	var meta *localFeedBatch
	var selectionErr error
	err = store.Update(now, func(state *feedevent.Reports) {
		expireFeedBacklog(ledger, state, now)
		open, backlog := batchCandidates(ledger, *state, now)
		selected := []feedevent.Entry{}
		for _, e := range append(open, backlog...) {
			r, prior := state.Exposure(e), before.Exposure(e)
			if r.HandedTime() != prior.HandedTime() || r.Hands() != prior.Hands() {
				continue
			}
			if len(selected) > 0 && e.ImpressionID != selected[0].ImpressionID {
				continue
			}
			selected = append(selected, e)
			if len(selected) == n {
				break
			}
		}
		if len(selected) == 0 {
			if len(open)+len(backlog) > 0 {
				selectionErr = fmt.Errorf("another invocation handed this batch; retry feed next")
			}
			return
		}
		page, loadErr := ledger.Page(selected[0].ImpressionID)
		if loadErr != nil {
			selectionErr = loadErr
			return
		}
		meta = &localFeedBatch{Source: "local", ImpressionID: selected[0].ImpressionID, Handed: len(selected), Open: len(open), Backlog: len(backlog), NextCommand: batchNextCommand(server, n)}
		var rehanded int64
		for _, e := range selected {
			if state.Exposure(e).HandedTime() == 0 {
				meta.Backlog--
			} else {
				meta.Open--
				rehanded++
			}
		}
		payload, selectionErr = makeFeedBatch(server, page, selected, *state, *meta)
		if selectionErr != nil {
			return
		}
		for _, e := range selected {
			r := state.Exposure(e)
			r.Hand(now)
			state.Entries[r.ImpressionID+"\x00"+r.ItemID] = r
		}
		counts := map[string]int64{"batches_handed": 1, "rehanded_items": rehanded}
		if poll {
			state.LocalPolls++
			counts["local_polls"] = 1
		}
		state.Count(now, counts)
	})
	if err != nil {
		return nil, nil, err
	}
	return payload, meta, selectionErr
}

// Cache failure or ledger failure preserves the existing whole-page delivery.
func saveAndBatchFeed(cmd *cobra.Command, server string, payload json.RawMessage, n int) error {
	if err := cache.SaveFeedResponse(server, payload); err != nil {
		warnReportLedger(err)
		warnReportLedger(recordFeedPull(server, payload))
		return renderFeedV2(cmd, payload)
	}
	cache.Cleanup(server, "broadcasts")
	now := time.Now().UnixMilli()
	ledger := feedLedger(server)
	var batch json.RawMessage
	var meta localFeedBatch
	var batchErr error
	err := feedReportStore(server).Update(now, func(state *feedevent.Reports) {
		entries, recordErr := recordFeedPage(state, payload, now, n)
		if recordErr != nil {
			batchErr = recordErr
			return
		}
		expireFeedBacklog(ledger, state, now)
		open, backlog := batchCandidates(ledger, *state, now)
		meta = localFeedBatch{Source: "server", Handed: len(entries), Open: len(open), Backlog: len(backlog), NextCommand: batchNextCommand(server, n)}
		var page struct {
			ImpressionID string `json:"impression_id"`
		}
		_ = json.Unmarshal(payload, &page)
		meta.ImpressionID = page.ImpressionID
		for _, e := range open {
			for _, selected := range entries {
				if e.ImpressionID == selected.ImpressionID && e.ItemID == selected.ItemID {
					meta.Open--
					break
				}
			}
		}
		batch, batchErr = makeFeedBatch(server, payload, entries, *state, meta)
	})
	if err != nil || batchErr != nil {
		warnReportLedger(err)
		warnReportLedger(batchErr)
		warnReportLedger(recordFeedPull(server, payload))
		return renderFeedV2(cmd, payload)
	}
	return renderFeedV2Batch(cmd, batch, &meta, false)
}

var feedNextCmd = &cobra.Command{Use: "next", Short: "Read the next batch of a saved Feed page", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
	n, err := feedBatchSize(cmd)
	if err != nil {
		return err
	}
	server := activeServerName()
	if server == "" {
		return fmt.Errorf("no active server")
	}
	if _, err := auth.LoadV2Credentials(server); err != nil {
		return err
	}
	payload, meta, err := localFeedBatchPayload(server, feedLedger(server), n, false)
	if err != nil {
		return err
	}
	if meta == nil {
		if resolveFormat() == "agent" {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "backlog=0 open=0")
		} else {
			output.PrintData(map[string]int{"backlog": 0, "open": 0}, resolveFormat())
		}
		return nil
	}
	return renderFeedV2Batch(cmd, payload, meta, true)
}}

func init() {
	feedPollCmd.Flags().Int("batch", 0, "save V2 pages and hand out 1–20 items at a time")
	feedNextCmd.Flags().Int("batch", 5, "max saved items to hand out (1–20)")
	feedCmd.AddCommand(feedNextCmd)
}
