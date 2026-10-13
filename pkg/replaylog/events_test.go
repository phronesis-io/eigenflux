package replaylog

import "testing"

func TestTypedIdentityValidation(t *testing.T) {
	for _, k := range []string{"broadcast", "commission", "agent"} {
		s := ServedItem{PipelineVersion: "need_search_v1", SampleSchemaVersion: 2, RequestMode: "search", SourceKind: k, SourceID: 7, ContextID: 3}
		if k == "broadcast" {
			s.ItemID = 7
		}
		if err := s.Validate(); err != nil {
			t.Fatal(err)
		}
		s.ItemID = 9
		if err := s.Validate(); err == nil {
			t.Fatal("invalid typed identity accepted")
		}
	}
	if err := (ServedItem{ItemID: 7}).Validate(); err != nil {
		t.Fatal("old event rejected", err)
	}
}
