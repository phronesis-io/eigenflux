// Evaluate frozen public broadcasts locally. No production database or queue writes.
package main

import (
	"context"
	"eigenflux_server/pipeline/llm"
	"eigenflux_server/pkg/config"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type item struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	Notes   string `json:"notes"`
}
type record struct {
	ID         string              `json:"id"`
	Evaluation *llm.SlopEvaluation `json:"evaluation,omitempty"`
	Error      string              `json:"error,omitempty"`
}

func main() {
	input := flag.String("input", "", "frozen UGC JSON array")
	out := flag.String("out", "", "private result directory")
	workers := flag.Int("workers", 4, "bounded concurrency, 1-8")
	flag.Parse()
	if *input == "" || *out == "" || *workers < 1 || *workers > 8 {
		panic("input, out and workers 1-8 required")
	}
	data, err := os.ReadFile(*input)
	must(err)
	var items []item
	must(json.Unmarshal(data, &items))
	must(os.MkdirAll(*out, 0700))
	cfg := config.Load()
	reg, err := llm.LoadDefaultPrompts()
	must(err)
	must(llm.ValidateAllPrompts(reg))
	c := llm.NewClient(cfg, reg).WithModel(cfg.UGCSlopModel).WithReasoningOff()
	jobs := make(chan item)
	var wg sync.WaitGroup
	for i := 0; i < *workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for it := range jobs {
				// Decimal IDs prevent output path traversal.
				if it.ID == "" {
					panic("empty ID")
				}
				for _, r := range it.ID {
					if r < '0' || r > '9' {
						panic("nondecimal ID")
					}
				}
				path := filepath.Join(*out, it.ID+".json")
				if b, e := os.ReadFile(path); e == nil {
					var old record
					if json.Unmarshal(b, &old) == nil && old.Evaluation != nil && old.Evaluation.ContentHash == llm.SlopHash(it.Content+"\x00"+it.Notes) && old.Evaluation.Model == cfg.UGCSlopModel {
						continue
					}
				}
				result, e := c.ProcessUGCItem(context.Background(), it.Content, it.Notes)
				rec := record{ID: it.ID, Evaluation: result}
				if e != nil {
					rec.Error = llm.SlopFailureStage(e)
				}
				b, e := json.MarshalIndent(rec, "", "  ")
				must(e)
				must(os.WriteFile(path, b, 0600))
				fmt.Printf("item %s evaluated=%t\n", it.ID, result != nil)
			}
		}()
	}
	for _, it := range items {
		jobs <- it
	}
	close(jobs)
	wg.Wait()
}
func must(err error) {
	if err != nil {
		panic(err)
	}
}
