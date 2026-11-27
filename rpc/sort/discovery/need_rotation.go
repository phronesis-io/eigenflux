package discovery

import (
	"context"
	"encoding/binary"
	"hash/fnv"
	"sort"

	"eigenflux_server/pkg/need"
)

const (
	needRotationSalt    = "discovery-need-rotation-v1:"
	needRotationWindow  = 8
	needRotationPinned  = 4
	needRotationSliceMS = int64(60 * 60 * 1000)
)

// EligibleNeedReader lists every eligible Need without Active's five-Need bound.
type EligibleNeedReader interface {
	Eligible(context.Context, int64, []string, int64) ([]need.Snapshot, error)
}

// needRotation reports whether this broadcast-only recommendation uses rotation.
func (e *Engine) needRotation(owner int64, kinds []Kind) bool {
	if len(kinds) != 1 || kinds[0] != Broadcast || !bucketEnabled(needRotationSalt, owner, e.NeedRotationBPS) {
		return false
	}
	_, ok := e.Needs.(EligibleNeedReader)
	return ok
}

func needPriority(s need.Snapshot) float64 {
	in, err := s.ExecutionInput()
	if err != nil || in.Priority == nil {
		return 0
	}
	return *in.Priority
}

// rotateNeeds picks the Needs used for one request from every eligible Need,
// given in priority-desc, creation-desc order. Agents with at most
// needRotationWindow Needs keep all of them. Otherwise up to needRotationPinned
// Needs with priority > 0 are always selected, and the remaining slots rotate
// across Intents (newest first inside an Intent) with an offset that is stable
// within one hour slice and moves between slices.
func rotateNeeds(all []need.Snapshot, owner, now int64) []need.Snapshot {
	if len(all) <= needRotationWindow {
		return all
	}
	pinned := make([]need.Snapshot, 0, needRotationPinned)
	queues := map[int64][]need.Snapshot{}
	for _, s := range all {
		if len(pinned) < needRotationPinned && needPriority(s) > 0 {
			pinned = append(pinned, s)
			continue
		}
		queues[s.IntentID] = append(queues[s.IntentID], s)
	}
	ids := make([]int64, 0, len(queues))
	for id := range queues {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := append([]need.Snapshot(nil), pinned...)
	if len(ids) == 0 {
		return out
	}
	// k advances by one per slice from a per-Agent start. The Intent order
	// rotates by k and every Intent starts its own queue at k, so each window
	// spans as many Intents as it has room for and every Need recurs.
	h := fnv.New64a()
	_, _ = h.Write([]byte(needRotationSalt))
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(owner))
	_, _ = h.Write(b[:])
	k := h.Sum64()%uint64(1<<32) + uint64(now/needRotationSliceMS)
	for round := 0; len(out) < needRotationWindow; round++ {
		progress := false
		for i := range ids {
			q := queues[ids[(k+uint64(i))%uint64(len(ids))]]
			if round >= len(q) {
				continue
			}
			progress = true
			out = append(out, q[(k+uint64(round))%uint64(len(q))])
			if len(out) == needRotationWindow {
				break
			}
		}
		if !progress {
			break
		}
	}
	return out
}
