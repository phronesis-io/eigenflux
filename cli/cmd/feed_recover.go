package cmd

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"cli.eigenflux.ai/internal/feedevent"
	"cli.eigenflux.ai/internal/output"
	"github.com/spf13/cobra"
)

func recoveredFeedEntries(ledger *feedevent.Ledger, state feedevent.Reports, cycle string) []feedevent.Entry {
	entries := []feedevent.Entry{}
	for _, e := range ledger.Entries() {
		r := state.Exposure(e)
		if r.CycleID == cycle && r.ScoredAt > 0 && (r.Score == 1 || r.Score == 2) && !state.Surfaced(e.ItemID) {
			entries = append(entries, e)
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		a, b := state.Exposure(entries[i]), state.Exposure(entries[j])
		if a.CyclePosition != b.CyclePosition {
			return a.CyclePosition < b.CyclePosition
		}
		if a.ImpressionID != b.ImpressionID {
			return a.ImpressionID < b.ImpressionID
		}
		return a.Position < b.Position
	})
	return entries
}

func recoverNextCommand(server, cycle, after string) string {
	command := feedCommandPrefix(server) + " feed recover --cycle " + shellQuote(cycle) + " --after " + shellQuote(after)
	if resolveFormat() == "agent" {
		command += " --format agent"
	}
	return command
}

var feedRecoverCmd = &cobra.Command{Use: "recover", Short: "Recover scored, unsurfaced items without changing local state", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
	server := activeServerName()
	if server == "" {
		return fmt.Errorf("no active server")
	}
	state, err := feedReportStore(server).Read()
	if err != nil {
		return err
	}
	ledger, now := feedLedger(server), time.Now().UnixMilli()
	cycle, _ := cmd.Flags().GetString("cycle")
	if cycle == "" {
		type summary struct {
			ID string `json:"id"`
			feedevent.Cycle
			ItemsToPush int `json:"items_to_push"`
		}
		cycles := []summary{}
		matching := []string{}
		for id, c := range state.Cycles {
			if !c.Active(now) {
				continue
			}
			cycles = append(cycles, summary{id, c, len(recoveredFeedEntries(ledger, state, id))})
			if c.Origin == feedCycleOrigin() {
				matching = append(matching, id)
			}
		}
		if len(matching) != 1 {
			sort.Slice(cycles, func(i, j int) bool { return cycles[i].ID < cycles[j].ID })
			output.PrintData(map[string]any{"cycles": cycles}, resolveFormat())
			return nil
		}
		cycle = matching[0]
	}
	if !state.Cycles[cycle].Active(now) {
		return fmt.Errorf("Feed cycle %q is unavailable, closed or expired", cycle)
	}
	entries := recoveredFeedEntries(ledger, state, cycle)
	after, _ := cmd.Flags().GetString("after")
	if after != "" {
		// Accept a cursor even when a surface report arrived after the previous page.
		position := 0
		for _, r := range state.Entries {
			if r.CycleID == cycle && r.ItemID == after {
				position = max(position, r.CyclePosition)
			}
		}
		if position == 0 {
			return fmt.Errorf("--after item is not in this cycle")
		}
		for len(entries) > 0 && state.Exposure(entries[0]).CyclePosition <= position {
			entries = entries[1:]
		}
	}
	if len(entries) == 0 {
		if resolveFormat() == "agent" {
			fmt.Fprintf(cmd.OutOrStdout(), "[EIGENFLUX RECOVERED] cycle=%s items=0 remaining=0 next=\"\"\n", cycle)
		} else {
			output.PrintData(map[string]any{"cycle_id": cycle, "items": []any{}, "remaining": 0}, resolveFormat())
		}
		return nil
	}
	selected := entries[:1]
	for len(selected) < len(entries) && entries[len(selected)].ImpressionID == selected[0].ImpressionID {
		selected = entries[:len(selected)+1]
	}
	page, err := ledger.Page(selected[0].ImpressionID)
	if err != nil {
		return err
	}
	meta := &localFeedBatch{Source: "local", ImpressionID: selected[0].ImpressionID, CycleID: cycle, Recovered: true, Remaining: len(entries)}
	_, payload, err := fitFeedBatch(server, page, selected, state, meta)
	if err != nil {
		return err
	}
	return renderFeedV2Batch(cmd, server, payload, meta)
}}

func init() {
	feedRecoverCmd.Flags().String("cycle", "", "open cycle to recover; otherwise select the only open cycle of this origin")
	feedRecoverCmd.Flags().String("after", "", "continue after this item from the preceding recovery page")
	feedCmd.AddCommand(feedRecoverCmd)
}

func addRecoveredContent(server string, item map[string]json.RawMessage, report feedevent.Report) error {
	item["impression_id"], _ = json.Marshal(report.ImpressionID)
	item["score"], _ = json.Marshal(report.Score)
	detail, err := loadFeedDetail(server, report.ItemID)
	if err != nil {
		return err
	}
	if len(detail) > 0 {
		item["fetched_detail"] = detail
	}
	return nil
}
