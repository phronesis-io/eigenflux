package cmd

import (
	"encoding/json"
	"fmt"
	"maps"
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
	feedRepeatReuseWindow     = 60 * time.Second
	feedBatchMax              = 20
	feedBatchOutputBudget     = 32 << 10
	feedBacklogAge            = 24 * time.Hour
	feedRehandLimit           = 3
	feedSurfaceReminderWindow = 2 * time.Hour
)

type localFeedBatch struct {
	CycleID       string `json:"cycle_id,omitempty"`
	Batch         int    `json:"batch,omitempty"`
	Overlap       string `json:"overlap,omitempty"`
	OversizeBytes int    `json:"oversize_bytes,omitempty"`
	Recovered     bool   `json:"recovered,omitempty"`
	Remaining     int    `json:"remaining,omitempty"`
	body          []byte

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
	return cycleBatchCandidates(ledger, state, now, "", false)
}

func cycleBatchCandidates(ledger *feedevent.Ledger, state feedevent.Reports, now int64, cycle string, isolate bool) (open, backlog []feedevent.Entry) {
	for _, e := range ledger.Entries() {
		r := state.Exposure(e)
		if isolate && r.CycleID != cycle && state.Cycles[r.CycleID].Active(now) {
			continue
		}
		if !e.Feed || r.Baseline || r.ServedAt <= now-feedBacklogAge.Milliseconds() || r.ServedAt > now {
			continue
		}
		if r.DeferredAt > 0 && (r.BacklogExpiredAt > 0 || r.CycleID == cycle || state.Cycles[r.CycleID].Active(now)) {
			continue
		}
		if r.HandedTime() == 0 {
			backlog = append(backlog, e)
		} else if r.Batched && r.ScoredAt == 0 && r.Delivery == "" && !state.Surfaced(e.ItemID) &&
			(r.DeferredAt > 0 || r.Hands() < feedRehandLimit) {
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

// Scheduled polls drain eligible saved work; explicit owner/more requests fetch.
func shouldFetchFeedBatch(action, origin string, open, backlog int) bool {
	return action == "more" || origin == publishOriginOwner || open+backlog == 0
}

func reusableFeedPage(action, origin string, state feedevent.Reports, now int64) (string, bool) {
	age := now - state.LastFetchAt
	return state.LastImpressionID, action != "more" && origin != publishOriginOwner &&
		state.LastFetchAt > 0 && age >= 0 && age < feedRepeatReuseWindow.Milliseconds() &&
		state.PageUnprocessed(state.LastImpressionID)
}

func expireFeedBacklog(ledger *feedevent.Ledger, state *feedevent.Reports, now int64) {
	counts := map[string]int64{}
	for _, e := range ledger.ReportEntries(*state) {
		r := state.Exposure(e)
		if !e.Feed || r.Baseline || r.BacklogExpiredAt > 0 || r.ServedAt > now-feedBacklogAge.Milliseconds() {
			continue
		}
		switch {
		case r.DeferredAt > 0:
			counts["deferred_expired_items"]++
		case r.HandedTime() == 0:
			counts["backlog_expired_items"]++
		default:
			continue
		}
		r.BacklogExpiredAt = now
		state.Entries[r.ImpressionID+"\x00"+r.ItemID] = r
	}
	if len(counts) > 0 {
		state.Count(now, counts)
	}
}

func feedCommandPrefix(server string) string {
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
	return prefix
}

func batchNextCommand(server string, n int, cycle string) string {
	command := fmt.Sprintf("%s feed next --batch %d", feedCommandPrefix(server), n)
	if cycle != "" {
		command += " --cycle " + shellQuote(cycle)
	}
	if resolveFormat() == "agent" {
		command += " --format agent"
	}
	return command
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
		if e.Position < 0 || e.Position >= len(items) {
			return nil, fmt.Errorf("cached Feed position is unavailable")
		}
		item := items[e.Position]
		var id string
		if json.Unmarshal(item["item_id"], &id) != nil || id != e.ItemID {
			return nil, fmt.Errorf("cached Feed item does not match its exposure")
		}
		item["already_surfaced"], _ = json.Marshal(state.Surfaced(e.ItemID))
		if meta.Recovered {
			if err := addRecoveredContent(server, item, state.Exposure(e)); err != nil {
				return nil, err
			}
		}
		batch = append(batch, item)
	}
	page["items"], _ = json.Marshal(batch)
	if err := filterFeedBatchMetadata(page, batch); err != nil {
		return nil, err
	}
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

func filterFeedBatchMetadata(page map[string]json.RawMessage, batch []map[string]json.RawMessage) error {
	if raw, ok := page["agent_card_updates"]; ok {
		var cards map[string]json.RawMessage
		if err := json.Unmarshal(raw, &cards); err != nil {
			return err
		}
		kept := map[string]json.RawMessage{}
		for _, item := range batch {
			var author struct {
				AgentID string `json:"agent_id"`
			}
			if json.Unmarshal(item["author_identity"], &author) == nil {
				if card, ok := cards[author.AgentID]; ok {
					kept[author.AgentID] = card
				}
			}
		}
		delete(page, "agent_card_updates")
		if len(kept) > 0 {
			page["agent_card_updates"], _ = json.Marshal(kept)
		}
	}
	if raw, ok := page["discovery"]; ok && string(raw) != "null" {
		var discovery map[string]json.RawMessage
		if err := json.Unmarshal(raw, &discovery); err != nil {
			return err
		}
		if rawItems, ok := discovery["items"]; ok {
			var entries []map[string]json.RawMessage
			if err := json.Unmarshal(rawItems, &entries); err != nil {
				return err
			}
			byID := map[string]map[string]json.RawMessage{}
			for _, entry := range entries {
				var id string
				_ = json.Unmarshal(entry["item_id"], &id)
				byID[id] = entry
			}
			kept := []map[string]json.RawMessage{}
			for _, item := range batch {
				var id string
				_ = json.Unmarshal(item["item_id"], &id)
				if entry, ok := byID[id]; ok {
					if sameFeedPreview(entry["preview"], item["preview"]) {
						delete(entry, "preview")
					}
					kept = append(kept, entry)
				}
			}
			discovery["items"], _ = json.Marshal(kept)
		}
		page["discovery"], _ = json.Marshal(discovery)
	}
	return nil
}

// Discovery previews carry only text while Feed previews add a truncation flag;
// a discovery preview is redundant when every field it has matches the item's.
func sameFeedPreview(discoveryRaw, itemRaw json.RawMessage) bool {
	var discovery, item map[string]json.RawMessage
	if json.Unmarshal(discoveryRaw, &discovery) != nil || json.Unmarshal(itemRaw, &item) != nil || len(discovery) == 0 {
		return false
	}
	for key, value := range discovery {
		var want, got any
		if json.Unmarshal(value, &want) != nil || json.Unmarshal(item[key], &got) != nil || want != got {
			return false
		}
	}
	return true
}

// Select and persist under the existing report lock. Revalidate the invocation's
// snapshot so a competing caller cannot turn newly handed items into its retries.
func localFeedBatchPayload(server string, ledger *feedevent.Ledger, n int, poll bool, requestedCycle string) (json.RawMessage, *localFeedBatch, error) {
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
		cycle, overlap, cycleErr := selectFeedCycle(*state, now, poll, requestedCycle)
		if cycleErr != nil {
			selectionErr = cycleErr
			return
		}
		expireFeedBacklog(ledger, state, now)
		open, backlog := cycleBatchCandidates(ledger, *state, now, cycle, true)
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
		meta = &localFeedBatch{Source: "local", ImpressionID: selected[0].ImpressionID, Open: len(open), Backlog: len(backlog), CycleID: cycle, Batch: state.Cycles[cycle].Batches + 1, Overlap: overlap, NextCommand: batchNextCommand(server, n, cycle)}
		selected, payload, selectionErr = fitFeedBatch(server, page, selected, *state, meta)
		if selectionErr != nil {
			meta = nil
			return
		}
		counts := handFeedBatch(state, selected, meta, now)

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
		original := state
		staged := *state
		staged.Entries = maps.Clone(state.Entries)
		staged.Cycles = maps.Clone(state.Cycles)
		staged.Health = append([]feedevent.HealthEvent(nil), state.Health...)
		state = &staged

		entries, recordErr := recordFeedPage(state, payload, now, n, true)
		if recordErr != nil {
			batchErr = recordErr
			return
		}
		cycle, overlap, cycleErr := selectFeedCycle(*state, now, true, "")
		if cycleErr != nil {
			batchErr = cycleErr
			return
		}
		expireFeedBacklog(ledger, state, now)
		open, backlog := cycleBatchCandidates(ledger, *state, now, cycle, true)
		if len(entries) == 0 && len(open)+len(backlog) == 0 {
			*original = *state
			return
		}
		available := map[string]bool{}
		for _, e := range append(open, backlog...) {
			available[e.ImpressionID+"\x00"+e.ItemID] = true
		}
		filtered := entries[:0]
		for _, e := range entries {
			if available[e.ImpressionID+"\x00"+e.ItemID] {
				filtered = append(filtered, e)
				if len(filtered) == n {
					break
				}
			}
		}
		entries = filtered

		meta = localFeedBatch{Source: "server", Open: len(open), Backlog: len(backlog), CycleID: cycle, Batch: 1, Overlap: overlap, NextCommand: batchNextCommand(server, n, cycle)}
		var page struct {
			ImpressionID string `json:"impression_id"`
		}
		_ = json.Unmarshal(payload, &page)
		meta.ImpressionID = page.ImpressionID
		entries, batch, batchErr = fitFeedBatch(server, payload, entries, *state, &meta)
		if batchErr == nil {
			state.Count(now, handFeedBatch(state, entries, &meta, now))
			*original = *state
		}

	})
	if err != nil || batchErr != nil {
		warnReportLedger(err)
		warnReportLedger(batchErr)
		warnReportLedger(recordFeedPull(server, payload))
		return renderFeedV2(cmd, payload)
	}
	if meta.CycleID == "" {
		return renderFeedV2(cmd, payload)
	}
	return renderFeedV2Batch(cmd, server, batch, &meta)
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
	cycle, _ := cmd.Flags().GetString("cycle")
	payload, meta, err := localFeedBatchPayload(server, feedLedger(server), n, false, cycle)
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
	return renderFeedV2Batch(cmd, server, payload, meta)
}}

func init() {
	feedPollCmd.Flags().Int("batch", 0, "save V2 pages and hand out 1–20 items at a time")
	feedNextCmd.Flags().String("cycle", "", "continue this open Feed cycle")
	feedNextCmd.Flags().Int("batch", 5, "max saved items to hand out (1–20)")
	feedCmd.AddCommand(feedNextCmd)
}
