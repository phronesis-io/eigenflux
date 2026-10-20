package main

import (
	"bytes"
	"context"
	"eigenflux_server/pkg/featureindex"
	"eigenflux_server/rpc/sort/discovery"
	searchindex "eigenflux_server/rpc/sort/discovery/index"
	"encoding/json"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"testing"
)

func TestSequentialSourceAdvancesOnlyDeliveredGroups(t *testing.T) {
	c := discovery.Context{ID: 1}
	docs := []discovery.Document{{Ref: discovery.SourceRef{Type: discovery.Broadcast, ID: 10}, GroupID: 10}, {Ref: discovery.SourceRef{Type: discovery.Broadcast, ID: 11}, GroupID: 11}}
	s := &refillSnapshot{snapshot: snapshot{Inventory: storeDocs(docs), Seen: map[string]bool{"broadcast:10": false, "broadcast:11": false}}, HistoryComplete: true, RefillPools: map[int64][]storedDocument{1: storeDocs(docs)}, Truncated: map[int64]bool{1: true}}
	f := newSequentialSource(s)
	if err := f.consume(discovery.Candidate{Document: docs[0]}); err != nil {
		t.Fatal(err)
	}
	groups, complete, err := f.ConsumedGroups(context.Background(), 1)
	if err != nil || !complete || len(groups) != 1 || groups[0] != 10 {
		t.Fatal(groups, complete, err)
	}
	got, err := f.RecallUnseen(context.Background(), c, groups, nil, 1)
	if err != nil || len(got) != 1 || got[0].Ref.ID != 11 || f.poolIncomplete {
		t.Fatal(got, err)
	}
	if err := f.consume(discovery.Candidate{Document: docs[0]}); err == nil {
		t.Fatal("duplicate accepted")
	}
	_, _ = f.RecallUnseen(context.Background(), c, groups, nil, 80)
	if !f.poolIncomplete {
		t.Fatal("truncated pool reported as exhaustive")
	}
}

type captureRoundTrip func(*http.Request) (*http.Response, error)

func (f captureRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestCaptureFriendProjectionPreservesInt64Filters(t *testing.T) {
	raw := `{"size":200,"query":{"bool":{"filter":[{"terms":{"author_agent_id":[337404505098289153]}}]}},"sort":[{"created_at":"desc"}]}`
	transport := captureESTransport{next: captureRoundTrip(func(r *http.Request) (*http.Response, error) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), "337404505098289153") || !strings.Contains(string(b), `"_source":["id"]`) {
			t.Fatalf("changed identity/filter: %s", b)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}")), Header: make(http.Header)}, nil
	})}
	r, err := http.NewRequest("POST", "http://localhost/items-*/_search", strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	response, err := transport.RoundTrip(r)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
}

func TestSlimCaptureFingerprintMatchesServingContentVersion(t *testing.T) {
	for _, text := range []string{"中文 & <tag>\u2028\u2029", "quotes \" and slash \\"} {
		item := map[string]any{"id": int64(337404505098289153), "author_agent_id": int64(337404505098289157), "content": text, "summary": "summary"}
		slots := searchindex.Slots{ProviderRegion: []string{"CN"}, Lang: []string{"zh"}}
		b, err := json.Marshal(map[string]any{"item": item, "slots": slots})
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("python3", "-c", "import json,sys;from slim_es import fingerprint;x=json.load(sys.stdin);print(fingerprint(x['item'],x['slots'])[0])")
		cmd.Stdin = bytes.NewReader(b)
		got, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		want := featureindex.BroadcastContentHash(item["id"].(int64), item["author_agent_id"].(int64), text+"\nsummary", slots)
		if strings.TrimSpace(string(got)) != want {
			t.Fatalf("slim capture changed content version: %s != %s", got, want)
		}
	}
}
