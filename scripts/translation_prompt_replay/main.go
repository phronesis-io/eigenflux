// translation_prompt_replay exercises the production translation prompt offline.
// Outputs require human review; this is not an online semantic validator.
package main

import (
	"context"
	"eigenflux_server/pipeline/llm"
	"eigenflux_server/pkg/config"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

func main() {
	cfg := config.Load()
	if cfg.LLMApiKey == "" {
		fmt.Fprintln(os.Stderr, "LLM_API_KEY required")
		os.Exit(1)
	}
	c := llm.NewClient(cfg, nil).WithModel(cfg.LLMTranslateModel)
	inputs := []string{
		"The broadcast also addresses FTC criticism regarding canary test coverage by explicitly listing uncovered failure types in summaries.",
		"zero_source v0.4: unit tri-state, TRUNCATED_NONZERO, CONTENT_SUBSTITUTED relay model swap, canary coverage report; seeking real or synthetic rows",
		"The canaries detect only the planted failure classes; this does not prove complete coverage.",
		"FTC may be right. We have not established the cause; 12 of 40 rows remain untested.",
		"@FTC said: retain version v0.4 and return code TRUNCATED_NONZERO. Do not claim that a regulator reviewed this.",
		"The FTC (Federal Trade Commission) did not review this release.",
		"ABC reported a 12 ms regression in v2.1; the cause may be unrelated.",
	}
	type result struct {
		Source      string `json:"source"`
		Translation string `json:"translation"`
		Error       string `json:"error,omitempty"`
	}
	out := struct {
		Model   string   `json:"model"`
		Results []result `json:"results"`
	}{Model: cfg.LLMTranslateModel}
	failed := false
	if out.Model == "" {
		out.Model = cfg.LLMModel
	}
	for _, input := range inputs {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		text, err := c.TranslateToChinese(ctx, input)
		cancel()
		row := result{Source: input, Translation: text}
		if err != nil {
			row.Error = err.Error()
			failed = true
		}
		out.Results = append(out.Results, row)
		if err != nil {
			break
		}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		panic(err)
	}
	if failed {
		os.Exit(1)
	}
}
