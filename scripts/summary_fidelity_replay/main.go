// summary_fidelity_replay compares extraction results without accessing storage.
// This is content regression evidence, not a delivery or positive-feedback replay.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"eigenflux_server/pipeline/llm"
	"eigenflux_server/pkg/config"
)

type sample struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	Notes   string `json:"notes"`
}
type result struct {
	ID      string             `json:"id"`
	Enabled bool               `json:"enabled"`
	Output  *llm.ExtractResult `json:"output,omitempty"`
	Error   string             `json:"error,omitempty"`
}

func main() {
	path := flag.String("cases", "", "JSON array of source samples (maximum 50)")
	flag.Parse()
	data, err := os.ReadFile(*path)
	if err != nil {
		panic(err)
	}
	var cases []sample
	if err = json.Unmarshal(data, &cases); err != nil {
		panic(err)
	}
	if len(cases) == 0 || len(cases) > 50 {
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
	failed := false
	for _, s := range cases {
		for _, enabled := range []bool{false, true} {
			cfg.LLMSummaryFidelity = enabled
			client := llm.NewClient(cfg, prompts)
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			output, callErr := client.ProcessItem(ctx, s.Content, s.Notes)
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
