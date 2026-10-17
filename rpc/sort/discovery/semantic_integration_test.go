package discovery_test

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"eigenflux_server/pkg/es"
	"eigenflux_server/rpc/sort/discovery"
)

func TestProfileSemanticESReads(t *testing.T) {
	esURL := os.Getenv("DISCOVERY_TEST_ES")
	if esURL == "" {
		t.Skip("isolated DISCOVERY_TEST_ES required")
	}
	t.Setenv("ES_URL", esURL)
	if err := es.InitClient(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	prefix := fmt.Sprintf("profile-semantic-test-%d", time.Now().UnixNano())
	index, rollover := prefix+"-000001", prefix+"-000002"
	do := func(method, path, body string) {
		t.Helper()
		resp, err := es.Client.Transport.Perform(mustRequest(t, method, esURL+path, body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			t.Fatalf("%s %s: %d", method, path, resp.StatusCode)
		}
	}
	mapping := `{"mappings":{"properties":{"id":{"type":"long"},"group_id":{"type":"long"},"author_agent_id":{"type":"long"},"content":{"type":"text"},"summary":{"type":"text"},"created_at":{"type":"date"},"embedding":{"type":"dense_vector","dims":2,"index":true,"similarity":"cosine"}}}}`
	for _, name := range []string{index, rollover} {
		do("PUT", "/"+name, mapping)
		defer do("DELETE", "/"+name, "")
	}
	docs := map[int64]string{
		1: `{"id":1,"group_id":11,"author_agent_id":9,"content":"a","created_at":"2020-01-01T00:00:00Z","embedding":[1,0]}`,
		2: fmt.Sprintf(`{"id":2,"group_id":12,"author_agent_id":9,"content":"b","created_at":%q,"embedding":[0.6,0.8]}`, time.Now().UTC().Format(time.RFC3339)),
		3: `{"id":3,"group_id":13,"author_agent_id":9,"content":"c"}`,
		4: `{"id":4,"group_id":14,"author_agent_id":7,"content":"d","embedding":[1,0.1]}`,
	}
	for id, body := range docs {
		do("PUT", fmt.Sprintf("/%s/_doc/%d", index, id), body)
	}
	// The same item in a later rollover generation must not crowd out others.
	do("PUT", fmt.Sprintf("/%s/_doc/2", rollover), docs[2])
	do("POST", "/"+prefix+"-*/_refresh", "")

	s := &discovery.Source{BroadcastIndex: prefix + "-*"}
	got, err := s.Cosine(ctx, []float32{1, 0}, []int64{1, 2, 3, 99})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || math.Abs(got[1]-1) > 1e-6 || math.Abs(got[2]-0.6) > 1e-6 {
		t.Fatalf("cosine: %v", got)
	}
	recalled, err := s.RecallProfile(ctx, discovery.Context{OwnerID: 7, Filters: discovery.Filters{ExcludeAuthors: []string{"7"}}}, []float32{1, 0}, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	// Rollover duplicates are merged by source key downstream, as for lexical recall.
	ids, dup := []string{}, map[int64]bool{}
	for _, d := range recalled {
		if !dup[d.Ref.ID] {
			dup[d.Ref.ID] = true
			ids = append(ids, fmt.Sprint(d.Ref.ID))
		}
	}
	if strings.Join(ids, ",") != "1,2" {
		t.Fatalf("profile kNN must rank by cosine and keep author exclusion: %v", ids)
	}
	recent, err := s.RecallProfile(ctx, discovery.Context{OwnerID: 7, Filters: discovery.Filters{ExcludeAuthors: []string{"7"}}}, []float32{1, 0}, time.Now().Add(-30*24*time.Hour).UnixMilli(), 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range recent {
		if d.Ref.ID != 2 {
			t.Fatalf("recent-only lane returned old content: %+v", recent)
		}
	}
	if len(recent) == 0 {
		t.Fatal("recent-only lane missed recent content")
	}
}

func mustRequest(t *testing.T, method, url, body string) *http.Request {
	t.Helper()
	r, err := http.NewRequest(method, url, bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/json")
	return r
}
