package main

import (
	"context"
	"reflect"
	"testing"

	"eigenflux_server/pipeline/llm"
	"eigenflux_server/rpc/sort/discovery"
)

func TestSlopReplayOnlyBlocksEvaluatedUGC(t *testing.T) {
	one, zero := 1, 0
	docs := []discovery.Document{
		{Ref: discovery.SourceRef{Type: discovery.Broadcast, ID: 1}, Quality: .8},
		{Ref: discovery.SourceRef{Type: discovery.Broadcast, ID: 2}, Quality: .7},
		{Ref: discovery.SourceRef{Type: discovery.Broadcast, ID: 3}, Quality: .6},
		{Ref: discovery.SourceRef{Type: discovery.Broadcast, ID: 4}, Quality: .5},
	}
	snap := &snapshot{Classes: map[int64]string{1: "ugc", 2: "pgc", 3: "ugc", 4: "ugc"}, Hydration: []hydrationBatch{{Input: storeDocs(docs), Output: storeDocs(docs)}}}
	source := &slopFrozenSource{frozenSource: newFrozenSource(snap), decisions: map[int64]*llm.SlopEvaluation{
		1: {Result: llm.UGCExtractResult{IsSlop: &one}},
		2: {Result: llm.UGCExtractResult{IsSlop: &one}},
		3: {Result: llm.UGCExtractResult{IsSlop: &zero}},
	}}
	got, err := source.Hydrate(context.Background(), 10, discovery.Recommendation, docs)
	if err != nil {
		t.Fatal(err)
	}
	want := append([]discovery.Document(nil), docs...)
	want[0].Blocked, want[0].Quality = true, 0
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("gate changed fields outside evaluated UGC: %+v", got)
	}
	if docs[0].Blocked || docs[0].Quality != .8 {
		t.Fatal("mutated original frozen evidence")
	}
}
