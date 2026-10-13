// summary_fidelity_replay compares extraction results without accessing storage.
// This is content regression evidence, not a delivery or positive-feedback replay.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"eigenflux_server/pipeline/llm"
	"eigenflux_server/pkg/config"
)

type sample struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	Notes   string `json:"notes"`
	Summary string `json:"summary,omitempty"`
}
type result struct {
	ID      string             `json:"id"`
	Enabled bool               `json:"enabled"`
	Output  *llm.ExtractResult `json:"output,omitempty"`
	Error   string             `json:"error,omitempty"`
}

func main() {
	path := flag.String("cases", "", "JSON array of source samples (maximum 50)")
	audit := flag.Bool("audit-existing", false, "audit supplied summaries without regenerating ranking metadata")
	workers := flag.Int("workers", 1, "audit-only concurrency (1-32)")
	flag.Parse()
	data, err := os.ReadFile(*path)
	if err != nil {
		panic(err)
	}
	var cases []sample
	if err = json.Unmarshal(data, &cases); err != nil {
		panic(err)
	}
	if len(cases) == 0 || (!*audit && len(cases) > 50) || len(cases) > 10000 || *workers < 1 || *workers > 32 {
		panic("provide 1-50 cases")
	}
	cfg := config.Load()
	if cfg.LLMApiKey == "" {
		panic("LLM_API_KEY required")
	}
	prompts, err := llm.LoadDefaultPrompts()
	if err != nil {
		panic(err)
	}
	enc := json.NewEncoder(os.Stdout)
	if *audit {
		client := llm.NewClient(cfg, prompts)
		failed := false
		var mu sync.Mutex
		work := make(chan sample)
		var wg sync.WaitGroup
		for i := 0; i < *workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for s := range work {
					start := time.Now()
					ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
					corrected, e := client.GroundSummary(ctx, s.Content, s.Notes, s.Summary)
					cancel()
					row := map[string]any{"id": s.ID, "summary": corrected, "elapsed_ms": time.Since(start).Milliseconds()}
					mu.Lock()
					if e != nil {
						row["error"] = "audit failed"
						row["error_code"] = auditErrorCode(e)
						failed = true
					}
					if err := enc.Encode(row); err != nil {
						panic(err)
					}
					mu.Unlock()
				}
			}()
		}
		for _, s := range cases {
			work <- s
		}
		close(work)
		wg.Wait()
		if failed {
			os.Exit(1)
		}
		return
	}
	failed := false
	for _, s := range cases {
		for _, enabled := range []bool{false, true} {
			cfg.LLMSummaryFidelity = enabled
			client := llm.NewClient(cfg, prompts)
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			output, callErr := client.ProcessPGCItem(ctx, s.Content, s.Notes)
			cancel()
			r := result{ID: s.ID, Enabled: enabled, Output: output}
			if callErr != nil {
				r.Error = "extraction failed (details withheld; inspect provider diagnostics)"
				failed = true
			}
			if err := enc.Encode(r); err != nil {
				panic(err)
			}
		}
	}
	if failed {
		fmt.Fprintln(os.Stderr, "one or more extraction calls failed")
		os.Exit(1)
	}
}

// Keep operational failure classes without serializing provider payloads.
func auditErrorCode(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	for _, label := range []string{"unsupported claims remain", "missing verdict", "contradictory verdict", "empty summary"} {
		if strings.Contains(err.Error(), "summary grounding: "+label) {
			return strings.ReplaceAll(label, " ", "_")
		}
	}
	return "provider_or_parse"
}
