// Replay changed PGC bodies through extraction and embeddings without storage.
package main

import (
	"context"
	"eigenflux_server/pipeline/embedding"
	"eigenflux_server/pipeline/llm"
	"eigenflux_server/pkg/config"
	"encoding/json"
	"flag"
	"os"
	"sync"
	"time"
)

type sample struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	Notes   string `json:"notes"`
	Enabled bool   `json:"enabled"`
}
type result struct {
	Sample   sample             `json:"sample"`
	Output   *llm.ExtractResult `json:"output,omitempty"`
	Vector   []float32          `json:"vector,omitempty"`
	Error    string             `json:"error,omitempty"`
	Attempts int                `json:"attempts"`
}

func main() {
	path := flag.String("cases", "", "paired source bodies")
	flag.Parse()
	b, err := os.ReadFile(*path)
	if err != nil {
		panic(err)
	}
	var cases []sample
	if err = json.Unmarshal(b, &cases); err != nil {
		panic(err)
	}
	if len(cases) == 0 || len(cases) > 2000 {
		panic("provide 1-2000 arms")
	}
	cfg := config.Load()
	prompts, err := llm.LoadDefaultPrompts()
	if err != nil {
		panic(err)
	}
	clients := map[bool]*llm.Client{}
	for _, on := range []bool{false, true} {
		copy := *cfg
		copy.LLMSummaryFidelity = on
		clients[on] = llm.NewClient(&copy, prompts)
	}
	emb := embedding.NewClient(cfg.EmbeddingProvider, cfg.EmbeddingApiKey, cfg.EmbeddingBaseURL, cfg.EmbeddingModel, cfg.EmbeddingDimensions)
	jobs := make(chan sample)
	var wg sync.WaitGroup
	var mu sync.Mutex
	failed := false
	enc := json.NewEncoder(os.Stdout)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for s := range jobs {
				r := result{Sample: s}
				for n := 1; n <= 3; n++ {
					r.Attempts = n
					ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
					// Each worker keeps its own error; shared clients are immutable.
					out, e := clients[s.Enabled].ProcessPGCItem(ctx, s.Content, s.Notes)
					cancel()
					r.Output = out
					if e == nil {
						r.Error = ""
						break
					}
					r.Error = "extraction_failed"
				}
				if r.Error == "" && !r.Output.Discard {
					for n := 0; n < 3; n++ {
						ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
						v, e := emb.GetEmbedding(ctx, s.Content)
						cancel()
						if e == nil && len(v) == emb.Dimensions() {
							r.Vector = v
							break
						}
						r.Error = "embedding_failed"
					}
					if len(r.Vector) > 0 {
						r.Error = ""
					}
				}
				mu.Lock()
				if r.Error != "" {
					failed = true
				}
				if e := enc.Encode(r); e != nil {
					panic(e)
				}
				mu.Unlock()
			}
		}()
	}
	for _, s := range cases {
		jobs <- s
	}
	close(jobs)
	wg.Wait()
	if failed {
		os.Exit(1)
	}
}
