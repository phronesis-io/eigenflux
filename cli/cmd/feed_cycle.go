package cmd

import (
	"crypto/rand"
	"encoding/json"
	"fmt"

	"cli.eigenflux.ai/internal/feedevent"
)

func feedCycleOrigin() string {
	if originFlag == "" {
		return publishOriginHeartbeat
	}
	return originFlag
}

func latestFeedCycle(state feedevent.Reports, now int64, origin string) string {
	latest := ""
	for id, c := range state.Cycles {
		if c.Active(now) && c.Origin == origin && (latest == "" || c.OpenedAt > state.Cycles[latest].OpenedAt || c.OpenedAt == state.Cycles[latest].OpenedAt && id > latest) {
			latest = id
		}
	}
	return latest
}

func selectFeedCycle(state feedevent.Reports, now int64, poll bool, requested []string) (string, string, error) {
	if poll {
		var suffix [2]byte
		if _, err := rand.Read(suffix[:]); err != nil {
			return "", "", err
		}
		return fmt.Sprintf("cyc_%d_%x", now, suffix), latestFeedCycle(state, now, feedCycleOrigin()), nil
	}
	if len(requested) > 0 && requested[0] != "" {
		if !state.Cycles[requested[0]].Active(now) {
			return "", "", fmt.Errorf("Feed cycle %q is unavailable, closed or expired", requested[0])
		}
		return requested[0], "", nil
	}
	return latestFeedCycle(state, now, feedCycleOrigin()), "", nil
}

func (m localFeedBatch) line() string {
	if m.Recovered {
		return fmt.Sprintf("[EIGENFLUX RECOVERED] cycle=%s items=%d remaining=%d next=%q%s These items are already scored; do not score them again. Earlier analysis was not saved; write it again from this content.", m.CycleID, m.Handed, m.Remaining, m.NextCommand, m.oversize())
	}
	line := fmt.Sprintf("[EIGENFLUX LOCAL BATCH] impression_id=%s items=%d open=%d backlog=%d source=%s", m.ImpressionID, m.Handed, m.Open, m.Backlog, m.Source)
	if m.CycleID != "" {
		line += fmt.Sprintf(" cycle=%s batch=%d", m.CycleID, m.Batch)
	}
	if m.Overlap != "" {
		line += " overlap=" + m.Overlap
	}
	return line + m.oversize() + fmt.Sprintf(" next=%q", m.NextCommand)
}

func (m localFeedBatch) oversize() string {
	if m.OversizeBytes > 0 {
		return fmt.Sprintf(" oversize=%d", m.OversizeBytes)
	}
	return ""
}

// Measure the exact body before claiming any items. Removed candidates retain
// their original state and order for the next handout.
func fitFeedBatch(server string, page json.RawMessage, selected []feedevent.Entry, state feedevent.Reports, meta *localFeedBatch) ([]feedevent.Entry, json.RawMessage, error) {
	base := *meta
	for {
		*meta = base
		meta.Handed = len(selected)
		if meta.Recovered {
			meta.Remaining = base.Remaining - len(selected)
			if meta.Remaining > 0 {
				meta.NextCommand = recoverNextCommand(server, meta.CycleID, selected[len(selected)-1].ItemID)
			}
		} else {
			for _, e := range selected {
				if state.Exposure(e).HandedTime() == 0 {
					meta.Backlog--
				} else {
					meta.Open--
				}
			}
		}
		payload, err := makeFeedBatch(server, page, selected, state, *meta)
		if err != nil {
			return nil, nil, err
		}
		body, err := feedBatchBody(server, payload, meta)
		if err != nil {
			return nil, nil, err
		}
		if len(body) > feedBatchOutputBudget && len(selected) > 1 {
			selected = selected[:len(selected)-1]
			continue
		}
		if len(body) > feedBatchOutputBudget {
			for meta.OversizeBytes != len(body) {
				meta.OversizeBytes = len(body)
				payload, err = makeFeedBatch(server, page, selected, state, *meta)
				if err != nil {
					return nil, nil, err
				}
				body, err = feedBatchBody(server, payload, meta)
				if err != nil {
					return nil, nil, err
				}
			}
		}
		meta.body = body
		return selected, payload, nil
	}
}

func handFeedBatch(state *feedevent.Reports, selected []feedevent.Entry, meta *localFeedBatch, now int64) map[string]int64 {
	counts := map[string]int64{"batches_handed": 1, "batch_body_bytes": int64(len(meta.body)), "batch_body_max_bytes": int64(len(meta.body))}
	if meta.OversizeBytes > 0 {
		counts["oversize_batches"] = 1
	}
	if meta.Overlap != "" {
		counts["overlapping_cycles"] = 1
	}
	cycle := state.Cycles[meta.CycleID]
	if cycle.OpenedAt == 0 {
		cycle = feedevent.Cycle{Origin: feedCycleOrigin(), OpenedAt: now}
	}
	cycle.Batches++
	for _, e := range selected {
		r := state.Exposure(e)
		if r.HandedTime() > 0 {
			counts["rehanded_items"]++
		}
		if r.CycleID != meta.CycleID || r.CyclePosition == 0 {
			cycle.Items++
			r.CyclePosition = cycle.Items
		}
		r.CycleID = meta.CycleID
		r.Hand(now)
		state.Entries[r.ImpressionID+"\x00"+r.ItemID] = r
	}
	if meta.CycleID != "" {
		if state.Cycles == nil {
			state.Cycles = map[string]feedevent.Cycle{}
		}
		state.Cycles[meta.CycleID] = cycle
	}
	return counts
}
