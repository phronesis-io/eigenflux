// Command discovery_load measures broadcast recall on synthetic loopback ES data.
// It never connects to a non-loopback host or uses application/user credentials.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"eigenflux_server/pkg/es"
	"eigenflux_server/rpc/sort/discovery"
	elasticsearch "github.com/elastic/go-elasticsearch/v8"
	"github.com/elastic/go-elasticsearch/v8/esapi"
)

var topics = []string{"eigenflux", "design", "engineering", "automation", "agents"}

const prefix = "discovery-load-"

type meter struct {
	base                http.RoundTripper
	calls, active, peak atomic.Int64
}

func (m *meter) RoundTrip(r *http.Request) (*http.Response, error) {
	tracked := strings.HasSuffix(r.URL.Path, "/_search")
	if tracked {
		m.calls.Add(1)
		n := m.active.Add(1)
		for {
			p := m.peak.Load()
			if n <= p || m.peak.CompareAndSwap(p, n) {
				break
			}
		}
	}
	response, err := m.base.RoundTrip(r)
	if tracked {
		m.active.Add(-1)
	}
	return response, err
}

func loopbackEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("plain loopback http endpoint required")
	}
	if u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1" {
		return fmt.Errorf("only literal loopback hosts are permitted")
	}
	return nil
}
func decodeResponse(r *esapi.Response, err error, v any) error {
	if err != nil {
		return err
	}
	defer func() { _ = r.Body.Close() }()
	if r.StatusCode >= 300 {
		return fmt.Errorf("ES HTTP %d", r.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(r.Body, 16<<20)).Decode(v)
}
func seed(ctx context.Context, documents, indices int) error {
	settings := `{"settings":{"number_of_shards":1,"number_of_replicas":0,"refresh_interval":"-1"},"mappings":{"properties":{"id":{"type":"long"},"author_agent_id":{"type":"long"},"content":{"type":"text"},"summary":{"type":"text"},"keywords":{"type":"keyword","fields":{"text":{"type":"text"}}},"lang":{"type":"keyword"},"created_at":{"type":"date"},"expire_time":{"type":"date"}}}}`
	for i := 0; i < indices; i++ {
		response, err := es.Client.Indices.Create(fmt.Sprintf("%s%03d", prefix, i), es.Client.Indices.Create.WithContext(ctx), es.Client.Indices.Create.WithBody(strings.NewReader(settings)))
		var result map[string]any
		if err := decodeResponse(response, err, &result); err != nil {
			return err
		}
	}
	now := time.Now().UTC()
	var bulk bytes.Buffer
	flush := func() error {
		if bulk.Len() == 0 {
			return nil
		}
		response, err := es.Client.Bulk(&bulk, es.Client.Bulk.WithContext(ctx))
		var result struct {
			Errors bool `json:"errors"`
		}
		if err := decodeResponse(response, err, &result); err != nil {
			return err
		}
		if result.Errors {
			return fmt.Errorf("bulk contained item failures")
		}
		bulk.Reset()
		return nil
	}
	for i := 0; i < documents; i++ {
		index := i % indices
		age := time.Duration(indices-index) * 24 * time.Hour
		created := now.Add(-age - time.Duration(i%86400)*time.Second)
		topic := topics[(i/indices)%len(topics)]
		expiry := now.Add(24 * time.Hour)
		if (i/indices)%3 == 0 {
			expiry = now.Add(-24 * time.Hour)
		}
		summary := topic + " agent workflow software research practical tooling"
		if index < indices-7 {
			summary = strings.Repeat(topic+" ", 20) + summary
		}
		// Include variable, benign prose so compressed source size is not constant.
		text := fmt.Sprintf("%s %s ref%d", topic, strings.Repeat("implementation coordination analysis delivery relevance context ", 10), i)
		doc := map[string]any{"id": i + 1, "author_agent_id": i%700 + 1, "content": text, "summary": summary, "keywords": []string{topic, "agents"}, "lang": "en", "created_at": created, "expire_time": expiry}
		metadata, _ := json.Marshal(map[string]any{"index": map[string]any{"_index": fmt.Sprintf("%s%03d", prefix, index), "_id": fmt.Sprint(i + 1)}})
		raw, _ := json.Marshal(doc)
		bulk.Write(metadata)
		bulk.WriteByte('\n')
		bulk.Write(raw)
		bulk.WriteByte('\n')
		if (i+1)%1000 == 0 {
			if err := flush(); err != nil {
				return err
			}
		}
		if (i+1)%100000 == 0 {
			log.Printf("seeded %d/%d", i+1, documents)
		}
	}
	if err := flush(); err != nil {
		return err
	}
	response, err := es.Client.Indices.Refresh(es.Client.Indices.Refresh.WithIndex(prefix+"*"), es.Client.Indices.Refresh.WithContext(ctx))
	var result map[string]any
	return decodeResponse(response, err, &result)
}

func query(ctx context.Context, c discovery.Context, channel, variant string) error {
	q, err := discovery.Query(c, discovery.Broadcast, channel, 80)
	if channel == "lexical_recent" {
		q, err = discovery.Query(c, discovery.Broadcast, channel, 20)
	}
	if err != nil {
		return err
	}
	if variant == "original" {
		b := q["query"].(map[string]any)["bool"].(map[string]any)
		filters := b["filter"].([]any)
		b["filter"] = filters[1:]
	}
	raw, err := json.Marshal(q)
	if err != nil {
		return err
	}
	response, err := es.Client.Search(es.Client.Search.WithContext(ctx), es.Client.Search.WithIndex(prefix+"*"), es.Client.Search.WithBody(bytes.NewReader(raw)))
	var result struct {
		TimedOut bool `json:"timed_out"`
		Shards   struct {
			Failed int `json:"failed"`
		} `json:"_shards"`
		Hits struct {
			Hits []json.RawMessage `json:"hits"`
		} `json:"hits"`
	}
	if err := decodeResponse(response, err, &result); err != nil {
		return err
	}
	if result.TimedOut || result.Shards.Failed > 0 {
		return fmt.Errorf("incomplete search")
	}
	return nil
}

type nodeStats struct {
	Nodes map[string]struct {
		Process struct {
			CPU struct {
				Total int64 `json:"total_in_millis"`
			} `json:"cpu"`
		} `json:"process"`
		JVM struct {
			Mem struct {
				HeapPercent int `json:"heap_used_percent"`
			} `json:"mem"`
			GC struct {
				Collectors map[string]struct {
					Time int64 `json:"collection_time_in_millis"`
				} `json:"collectors"`
			} `json:"gc"`
		} `json:"jvm"`
		ThreadPool map[string]struct{ Queue, Rejected, Active int64 } `json:"thread_pool"`
		Indices    struct {
			Search struct {
				Queries int64 `json:"query_total"`
				Time    int64 `json:"query_time_in_millis"`
			} `json:"search"`
		} `json:"indices"`
	} `json:"nodes"`
}

func stats() nodeStats {
	var result nodeStats
	response, err := es.Client.Nodes.Stats(es.Client.Nodes.Stats.WithMetric("process", "jvm", "thread_pool", "indices"))
	if err := decodeResponse(response, err, &result); err != nil {
		log.Fatal(err)
	}
	return result
}
func totals(s nodeStats) (cpu, gc, rejections, shards int64) {
	for _, n := range s.Nodes {
		cpu += n.Process.CPU.Total
		for _, c := range n.JVM.GC.Collectors {
			gc += c.Time
		}
		rejections += n.ThreadPool["search"].Rejected
		shards += n.Indices.Search.Queries
	}
	return
}

type result struct {
	Variant      string  `json:"variant"`
	Concurrency  int     `json:"concurrent_recommendations"`
	Requests     int     `json:"recommendations"`
	Contexts     int     `json:"contexts_per_recommendation"`
	Errors       int64   `json:"recommendations_with_errors"`
	ErrorExample string  `json:"error_example,omitempty"`
	ESRequests   int64   `json:"es_requests"`
	PeakClient   int64   `json:"peak_client_queries"`
	Seconds      float64 `json:"wall_seconds"`
	RPS          float64 `json:"completed_recommendations_per_second"`
	P50          float64 `json:"p50_ms"`
	P95          float64 `json:"p95_ms"`
	P99          float64 `json:"p99_ms"`
	CPU          int64   `json:"es_process_cpu_ms"`
	GC           int64   `json:"es_gc_ms"`
	Rejected     int64   `json:"search_rejections"`
	ShardQueries int64   `json:"shard_queries"`
	MaxQueue     int64   `json:"max_sampled_search_queue"`
	MaxHeap      int     `json:"max_sampled_heap_percent"`
}

func run(m *meter, variant string, concurrency, requests int) result {
	before := stats()
	m.calls.Store(0)
	m.peak.Store(0)
	var errors atomic.Int64
	var firstError string
	var mu sync.Mutex
	latencies := make([]float64, requests)
	source := &discovery.Source{BroadcastIndex: prefix + "*"}
	started := time.Now()
	stop := make(chan struct{})
	var monitor sync.WaitGroup
	monitor.Add(1)
	var queueMax int64
	var heapMax int
	go func() {
		defer monitor.Done()
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				s := stats()
				for _, n := range s.Nodes {
					if n.ThreadPool["search"].Queue > queueMax {
						queueMax = n.ThreadPool["search"].Queue
					}
					if n.JVM.Mem.HeapPercent > heapMax {
						heapMax = n.JVM.Mem.HeapPercent
					}
				}
			}
		}
	}()
	work := make(chan int)
	var workers sync.WaitGroup
	for w := 0; w < concurrency; w++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for job := range work {
				at := time.Now()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				slots := make(chan struct{}, 6)
				recent := make(chan struct{}, 1)
				var calls sync.WaitGroup
				var failed atomic.Bool
				for _, topic := range topics {
					c := discovery.Context{Query: topic, Filters: discovery.Filters{ExcludeAuthors: []string{fmt.Sprint(job%700 + 1)}}}
					channels := []string{"lexical"}
					if variant == "separate_recent" || variant == "bounded_recent" {
						channels = append(channels, "lexical_recent")
					}
					for _, channel := range channels {
						c, channel := c, channel

						calls.Add(1)
						go func() {
							defer calls.Done()
							workCtx := ctx
							if variant == "bounded_recent" && channel == "lexical_recent" {
								var cancel context.CancelFunc
								workCtx, cancel = context.WithTimeout(ctx, time.Second)
								defer cancel()
								select {
								case recent <- struct{}{}:
									defer func() { <-recent }()
								case <-workCtx.Done():
									failed.Store(true)
									return
								}
							}
							select {
							case slots <- struct{}{}:
								defer func() { <-slots }()
							case <-workCtx.Done():
								failed.Store(true)
								return
							}
							var err error
							if variant == "bounded_recent" && channel == "lexical_recent" {
								_, err = source.Recall(workCtx, c, discovery.Broadcast, channel, 20)
							} else {
								err = query(ctx, c, channel, variant)
							}
							if err != nil {
								failed.Store(true)
								mu.Lock()
								if firstError == "" {
									firstError = err.Error()
								}
								mu.Unlock()
							}
						}()
					}
				}
				calls.Wait()
				cancel()
				if failed.Load() {
					errors.Add(1)
				}
				latencies[job] = float64(time.Since(at).Microseconds()) / 1000
			}
		}()
	}
	for i := 0; i < requests; i++ {
		work <- i
	}
	close(work)
	workers.Wait()
	seconds := time.Since(started).Seconds()
	close(stop)
	monitor.Wait()
	after := stats()
	sort.Float64s(latencies)
	pct := func(p float64) float64 { return latencies[int(math.Ceil(float64(len(latencies))*p))-1] }
	bc, bg, br, bq := totals(before)
	ac, ag, ar, aq := totals(after)
	return result{Variant: variant, Concurrency: concurrency, Requests: requests, Contexts: len(topics), Errors: errors.Load(), ErrorExample: firstError, ESRequests: m.calls.Load(), PeakClient: m.peak.Load(), Seconds: seconds, RPS: float64(requests) / seconds, P50: pct(.5), P95: pct(.95), P99: pct(.99), CPU: ac - bc, GC: ag - bg, Rejected: ar - br, ShardQueries: aq - bq, MaxQueue: queueMax, MaxHeap: heapMax}
}
func main() {
	endpoint := flag.String("url", "", "isolated loopback Elasticsearch endpoint")
	seedDocs := flag.Int("seed-docs", 0, "create synthetic indices and this many documents (0 reuses fixture)")
	indices := flag.Int("indices", 30, "synthetic single-shard indices")
	requests := flag.Int("requests", 40, "recommendations per variant/concurrency")
	concurrency := flag.Int("concurrency", 1, "simultaneous recommendations")
	variant := flag.String("variant", "expiry", "original, expiry, separate_recent or bounded_recent")
	flag.Parse()
	if err := loopbackEndpoint(*endpoint); err != nil {
		log.Fatal(err)
	}
	if *indices < 1 || *indices > 30 || *seedDocs < 0 || *seedDocs > 2000000 || *requests < 1 || *requests > 500 || *concurrency < 1 || *concurrency > 32 {
		log.Fatal("invalid bounded fixture/load parameters")
	}
	if *variant != "original" && *variant != "expiry" && *variant != "separate_recent" && *variant != "bounded_recent" {
		log.Fatal("invalid variant")
	}
	m := &meter{base: &http.Transport{MaxIdleConns: 128, MaxIdleConnsPerHost: 128, ResponseHeaderTimeout: 5 * time.Second, Proxy: nil}}
	client, err := elasticsearch.NewClient(elasticsearch.Config{Addresses: []string{*endpoint}, Transport: m, DisableRetry: true})
	if err != nil {
		log.Fatal(err)
	}
	es.Client = client
	if *seedDocs > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		if err := seed(ctx, *seedDocs, *indices); err != nil {
			log.Fatal(err)
		}
	}
	// Warm all terms independently of the timed variant to reduce ordering bias.
	for _, topic := range topics {
		if err := query(context.Background(), discovery.Context{Query: topic, Filters: discovery.Filters{ExcludeAuthors: []string{"999"}}}, "lexical", "expiry"); err != nil {
			log.Fatal(err)
		}
	}
	if err := json.NewEncoder(os.Stdout).Encode(run(m, *variant, *concurrency, *requests)); err != nil {
		log.Fatal(err)
	}
}
