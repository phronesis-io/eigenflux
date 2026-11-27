package discovery

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"eigenflux_server/pkg/need"
)

func rotationFixture(id, intent int64, priority float64) need.Snapshot {
	in := need.Input{SchemaVersion: need.InputSchemaVersion, IntentID: intent, IntentVersion: 1, NeedType: string(Broadcast), Target: need.Target{Goal: "news"}}
	if priority > 0 {
		in.Priority = &priority
	}
	raw, _ := json.Marshal(in)
	return need.Snapshot{InputID: id, IntentID: intent, IntentVersion: 1, Input: raw}
}

func rotationIDs(in []need.Snapshot) []int64 {
	out := make([]int64, len(in))
	for i, s := range in {
		out[i] = s.InputID
	}
	return out
}

func rotationSet(n int, intents int64) []need.Snapshot {
	out := []need.Snapshot{}
	for i := 1; i <= n; i++ {
		out = append(out, rotationFixture(int64(i), int64(i)%intents+1, 0))
	}
	return out
}

type eligibleStore struct {
	*memStore
	eligible []need.Snapshot
}

func (s eligibleStore) Eligible(context.Context, int64, []string, int64) ([]need.Snapshot, error) {
	return s.eligible, nil
}

func TestEngineNeedRotationSwitch(t *testing.T) {
	e, _, store := engineFixture()
	all := []need.Snapshot{}
	for i := int64(1); i <= 12; i++ {
		s := rotationFixture(i, i, 0)
		if i <= 2 {
			s = rotationFixture(i, i, 1)
		}
		all = append(all, s)
		store.needs[i] = s
	}
	store.active = all[:5]
	e.Needs = eligibleStore{store, all}
	request := Request{SourceKinds: []Kind{Broadcast}}

	x, err := e.Execute(context.Background(), 1, request, Recommendation, 100)
	if err != nil || len(x.Contexts) != 5 || x.Contexts[0].Priority != 1 || x.Experiments != nil {
		t.Fatal("switch off must keep Active", len(x.Contexts), x.Experiments, err)
	}
	e.NeedRotationBPS = 10000
	x, err = e.Execute(context.Background(), 1, request, Recommendation, 100)
	if err != nil || len(x.Contexts) != needRotationWindow || x.Experiments["discovery-need-rotation-v1"] != "treatment" {
		t.Fatal("switch on must rotate", len(x.Contexts), x.Experiments, err)
	}
	for _, c := range x.Contexts {
		if c.Priority != 0 {
			t.Fatal("treatment Needs must share one page tier", c.Priority)
		}
	}
	x, err = e.Execute(context.Background(), 1, Request{SourceKinds: []Kind{Broadcast, Agent}}, Recommendation, 100)
	if err != nil || len(x.Contexts) < 5 || x.Contexts[0].Priority != 1 {
		t.Fatal("mixed-kind requests stay on Active", err)
	}
}

func TestRotateNeedsKeepsSmallSetsUnchanged(t *testing.T) {
	all := rotationSet(needRotationWindow, 3)
	if got := rotateNeeds(all, 7, 0); !reflect.DeepEqual(rotationIDs(got), rotationIDs(all)) {
		t.Fatal(rotationIDs(got))
	}
}

func TestRotateNeedsStableWithinSliceAndMovesAcrossSlices(t *testing.T) {
	all := rotationSet(34, 10)
	base := int64(1_795_000_000_000)
	base -= base % needRotationSliceMS
	first := rotationIDs(rotateNeeds(all, 7, base))
	if len(first) != needRotationWindow {
		t.Fatal(first)
	}
	if again := rotationIDs(rotateNeeds(all, 7, base+needRotationSliceMS-1)); !reflect.DeepEqual(first, again) {
		t.Fatal("window changed inside one slice", first, again)
	}
	covered := map[int64]bool{}
	moved := false
	for slice := int64(0); slice < 48; slice++ {
		got := rotationIDs(rotateNeeds(all, 7, base+slice*needRotationSliceMS))
		if !reflect.DeepEqual(got, first) {
			moved = true
		}
		for _, id := range got {
			covered[id] = true
		}
	}
	if !moved || len(covered) != len(all) {
		t.Fatal("rotation did not cover every Need", moved, len(covered))
	}
}

func TestRotateNeedsPinsPriorityNeeds(t *testing.T) {
	all := []need.Snapshot{}
	for i := int64(1); i <= 6; i++ {
		all = append(all, rotationFixture(i, i, 1.0-float64(i)/10))
	}
	all = append(all, rotationSet(20, 5)...)
	for i := range all[6:] {
		all[6+i].InputID += 100
	}
	for slice := int64(0); slice < 24; slice++ {
		got := rotationIDs(rotateNeeds(all, 9, slice*needRotationSliceMS))
		if len(got) != needRotationWindow || !reflect.DeepEqual(got[:needRotationPinned], []int64{1, 2, 3, 4}) {
			t.Fatal(got)
		}
	}
}

func TestRotateNeedsSpreadsAcrossIntents(t *testing.T) {
	all := []need.Snapshot{}
	for i := int64(1); i <= 12; i++ {
		all = append(all, rotationFixture(i, 1, 0))
	}
	for i := int64(13); i <= 16; i++ {
		all = append(all, rotationFixture(i, i, 0))
	}
	for slice := int64(0); slice < 24; slice++ {
		intents := map[int64]bool{}
		for _, s := range rotateNeeds(all, 3, slice*needRotationSliceMS) {
			intents[s.IntentID] = true
		}
		if len(intents) < 4 {
			t.Fatal("window concentrated on one Intent", slice, intents)
		}
	}
}
