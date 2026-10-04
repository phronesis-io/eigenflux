package delivery

import (
	"context"
	"testing"

	"eigenflux_server/pkg/itemstats"
	"eigenflux_server/rpc/sort/discovery"
)

func TestDiscoveryDeliveredBroadcastConsumption(t *testing.T) {
	for _, mode := range []discovery.Mode{discovery.Search, discovery.Recommendation} {
		t.Run(string(mode), func(t *testing.T) {
			s, _, r := setup(t)
			ctx := context.Background()
			for i := 0; i < 2; i++ {
				if _, err := s.Serve(ctx, 1, discovery.Request{Query: "test"}, mode, "retry"); err != nil {
					t.Fatal(err)
				}
			}
			await(t, func() bool { return r.XLen(ctx, itemstats.StreamName).Val() > 0 })
			entries, err := r.XRange(ctx, itemstats.StreamName, "-", "+").Result()
			if err != nil || len(entries) != 1 {
				t.Fatalf("one broadcast consumption, no typed IDs or retry duplicates: %v %v", entries, err)
			}
			event, err := itemstats.ParseEvent(entries[0].Values)
			if err != nil || event.EventType != itemstats.EventTypeConsumed || event.AgentID != 1 || event.ItemID != 77 {
				t.Fatal(event, err)
			}
		})
	}
}

func TestConsumptionFailureDoesNotBlockOtherRecording(t *testing.T) {
	s, _, r := setup(t)
	ctx := context.Background()
	if err := r.Set(ctx, itemstats.StreamName, "wrong-type", 0).Err(); err != nil {
		t.Fatal(err)
	}
	before := recordingFailures("consumed")
	if _, err := s.Serve(ctx, 1, discovery.Request{}, discovery.Recommendation, ""); err != nil {
		t.Fatal(err)
	}
	await(t, func() bool {
		return recordingFailures("consumed") == before+1 && r.XLen(ctx, "stream:replay:log").Val() == 1 && r.SIsMember(ctx, "impr:agent:1:items", "77").Val()
	})
}

func TestConsumptionOnlyCountsPreparedPages(t *testing.T) {
	s, e, r := setup(t)
	ctx := context.Background()
	base := e.x.Candidates[0]
	e.x.Candidates = nil
	for i := int64(0); i < 3; i++ {
		c := base
		c.Document.Ref.ID += i
		e.x.Candidates = append(e.x.Candidates, c)
	}
	prepare := func(_ context.Context, x *discovery.Execution) error {
		if x.Candidates[0].Document.Ref.ID == 78 {
			x.Candidates = nil
		}
		return nil
	}
	for i, action := range []string{"refresh", "load_more", "load_more"} {
		if _, _, err := s.ServePage(ctx, 1, action, 1, prepare); err != nil {
			t.Fatal(err)
		}
		want := int64(min(i+1, 2))
		await(t, func() bool { return r.XLen(ctx, itemstats.StreamName).Val() == want })
	}
	entries, err := r.XRange(ctx, itemstats.StreamName, "-", "+").Result()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Values["item_id"] == "78" {
			t.Fatal("skipped item was counted")
		}
	}
}
