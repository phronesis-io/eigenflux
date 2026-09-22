package discoveryserve

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"eigenflux_server/pkg/bloomfilter"
	"eigenflux_server/pkg/discovery"
	"eigenflux_server/pkg/impr"
	"eigenflux_server/pkg/logger"
	"eigenflux_server/pkg/metrics"
	"eigenflux_server/pkg/replaylog"
	"github.com/redis/go-redis/v9"
)

type historyEntry struct{ key, member string }
type claimEntry struct {
	key string
	ttl time.Duration
}

// Snapshot recording data before starting background work. Neither writer owns
// the response, and a failure in one must not prevent the other from running.
func (s Service) recordAsync(ctx context.Context, owner int64, x discovery.Execution, impression string, position int, now int64) {
	if len(x.Candidates) == 0 {
		return
	}
	history := []historyEntry{}
	claims := []claimEntry{}
	for _, c := range x.Candidates {
		d := c.Document
		if d.Ref.Type == discovery.Broadcast {
			key := fmt.Sprintf(impr.KeyItemIDs, owner)
			if x.Mode == discovery.Search {
				key = fmt.Sprintf("impr:search:agent:%d:items", owner)
			}
			history = append(history, historyEntry{key, fmt.Sprint(d.Ref.ID)})
			if x.Mode == discovery.Recommendation {
				if d.GroupID > 0 {
					history = append(history, historyEntry{fmt.Sprintf(impr.KeyGroupIDs, owner), fmt.Sprint(d.GroupID)})
					if !s.DisableDedup {
						history = append(history, historyEntry{bloomfilter.GetKeyForDate(time.Now()), fmt.Sprintf("%d:%d", owner, d.GroupID)})
					}
				}
				if d.URL != "" {
					history = append(history, historyEntry{fmt.Sprintf(impr.KeyURLs, owner), d.URL})
				}
				if c.ClaimTTLSeconds > 0 {
					claims = append(claims, claimEntry{fmt.Sprintf("sort:inject:claim:%d", d.Ref.ID), time.Duration(c.ClaimTTLSeconds) * time.Second})
				}
			}
		} else if x.Mode == discovery.Recommendation {
			history = append(history, historyEntry{fmt.Sprintf("impr:discovery:agent:%d:items", owner), d.Ref.Key()})
		}
	}
	bg := context.WithoutCancel(ctx)
	go func() {
		writeCtx, cancel := context.WithTimeout(bg, 2*time.Second)
		defer cancel()
		_, err := s.Redis.Pipelined(writeCtx, func(p redis.Pipeliner) error {
			for _, h := range history {
				p.SAdd(writeCtx, h.key, h.member)
				p.Expire(writeCtx, h.key, 30*24*time.Hour)
			}
			for _, c := range claims {
				p.SetNX(writeCtx, c.key, "1", c.ttl)
			}
			return nil
		})
		recordingError(writeCtx, "history", err)
	}()
	values, err := sampleValues(owner, x, impression, position, now)
	if err != nil {
		recordingError(ctx, "sample", err)
		return
	}
	go func() {
		writeCtx, cancel := context.WithTimeout(bg, 2*time.Second)
		defer cancel()
		err := s.Redis.XAdd(writeCtx, &redis.XAddArgs{Stream: replaylog.StreamName, MaxLen: s.StreamMaxLen, Approx: s.StreamMaxLen > 0, Values: values}).Err()
		recordingError(writeCtx, "sample", err)
	}()
}

func recordingError(ctx context.Context, stage string, err error) {
	if err == nil {
		return
	}
	metrics.DiscoveryRecordingFailures.WithLabelValues(stage).Inc()
	logger.Ctx(ctx).Warn("discovery recording failed", "stage", stage, "err", err)
}

func sampleValues(owner int64, x discovery.Execution, impression string, position int, now int64) (map[string]interface{}, error) {
	items := make([]replaylog.ServedItem, 0, len(x.Candidates))
	for pos, c := range x.Candidates {
		features, err := json.Marshal(map[string]any{"search": c})
		if err != nil {
			return nil, err
		}
		si := replaylog.ServedItem{PipelineVersion: discovery.PipelineVersion, RequestMode: string(x.Mode), SampleSchemaVersion: 2, SourceKind: string(c.Document.Ref.Type), SourceID: c.Document.Ref.ID, ContextID: c.Context.ID, ItemFeatures: string(features), Score: c.FinalScore, Position: pos + position}
		if c.Context.NeedID() != 0 {
			si.NeedID = c.Context.NeedID()
			si.NeedRevision = c.Context.Revision
			if c.Context.SourceNeedID > 0 {
				si.NeedRevision = c.Context.SourceNeedRevision
			}
		}
		if c.Document.Ref.Type == discovery.Broadcast {
			si.ItemID = c.Document.Ref.ID
		}
		if err := si.Validate(); err != nil {
			return nil, err
		}
		items = append(items, si)
	}
	sample, err := json.Marshal(items)
	if err != nil {
		return nil, err
	}
	contexts, err := json.Marshal(map[string]any{"pipeline_version": discovery.PipelineVersion, "search_context": map[string]any{"contexts": x.Contexts, "request_mode": x.Mode, "request_time": now, "fallback_reason": x.FallbackReason}})
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{"impression_id": impression, "agent_id": fmt.Sprint(owner), "agent_features": string(contexts), "served_at": fmt.Sprint(time.Now().UnixMilli()), "items": string(sample), "delivered": "1"}, nil
}
