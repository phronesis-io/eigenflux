package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestReplayPublisherSummaryContract(t *testing.T) {
	for _, raw := range []string{`{}`, `{"summary":null}`, `{"summary":3}`, `{"summary":"  "}`, `{"summary":"Source-linked report; see the full broadcast for details."}`, `{"summary":`} {
		if _, ok := replayPublisherSummary(raw); ok {
			t.Fatalf("accepted %s", raw)
		}
	}
	for _, n := range []int{280, 281} {
		b, _ := json.Marshal(map[string]string{"summary": "  " + strings.Repeat("字", n) + "  "})
		v, ok := replayPublisherSummary(string(b))
		if ok != (n == 280) {
			t.Fatal("wrong bound")
		}
		if ok && v != strings.Repeat("字", n) {
			t.Fatal("summary changed")
		}
	}
}
