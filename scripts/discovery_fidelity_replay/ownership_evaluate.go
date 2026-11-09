package main

import (
	"context"
	"eigenflux_server/pipeline/llm"
	"eigenflux_server/pkg/config"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type ownershipEvaluation struct {
	ID              int64              `json:"id,string"`
	InputHash       string             `json:"input_hash"`
	PromptHash      string             `json:"prompt_hash"`
	RequestDate     string             `json:"request_date"`
	MaxTokens       int                `json:"max_tokens"`
	Contract        string             `json:"contract"`
	Model           string             `json:"model"`
	Baseline        *llm.ExtractResult `json:"baseline"`
	BaselineBlocked bool               `json:"baseline_blocked"`
	Output          *llm.ExtractResult `json:"output"`
	Blocked         bool               `json:"blocked"`
	Error           string             `json:"error,omitempty"`
}

func ownershipPromptHash() string {
	b, e := os.ReadFile("static/templates/prompts/process_item_ugc.tmpl")
	must(e)
	return llm.SlopHash(string(b))
}
func evaluateOwnership(cfg *config.Config, dir string) {
	if !cfg.UGCSlopEnabled {
		panic("this evaluation requires the deployed UGC Slop route")
	}
	// Evaluation can run incrementally while capture continues. Comparison still
	// requires the complete fixed cohort and matching evidence for every candidate.
	snapshots := []*snapshot{}
	files, e := filepath.Glob(filepath.Join(dir, "agent-*.json"))
	must(e)
	for _, f := range files {
		b, e := os.ReadFile(f)
		must(e)
		var s snapshot
		must(json.Unmarshal(b, &s))
		if s.CaptureError == "" {
			snapshots = append(snapshots, &s)
		}
	}
	classes := map[int64]string{}
	for _, s := range snapshots {
		for id, c := range s.Classes {
			classes[id] = c
		}
	}
	var items []itemEvidence
	b, e := os.ReadFile(filepath.Join(dir, "items.json"))
	must(e)
	must(json.Unmarshal(b, &items))
	path := filepath.Join(dir, "ownership-evaluations.json")
	cached := map[int64]ownershipEvaluation{}
	if b, e := os.ReadFile(path); e == nil {
		must(json.Unmarshal(b, &cached))
	}
	prompts, e := llm.LoadPrompts("static/templates/prompts")
	must(e)
	client := llm.NewClient(cfg, prompts).WithModel(cfg.UGCSlopModel).WithReasoningOff()
	hash := ownershipPromptHash()
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for _, item := range items {
		if classes[item.ItemID] != "ugc" || !item.PotentiallyEligible {
			continue
		}
		if _, rejected := ownershipProjection(item, "ugc"); rejected {
			continue
		}
		mu.Lock()
		old, ok := cached[item.ItemID]
		mu.Unlock()
		if ok && old.InputHash == auditInputHash(item) && old.PromptHash == hash && old.Model == cfg.UGCSlopModel && old.RequestDate == time.Now().UTC().Format("2006-01-02") && old.MaxTokens == cfg.LLMMaxTokens && old.Contract == llm.SlopRequestContract && old.Baseline != nil && old.Error == "" {
			continue
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(item itemEvidence) {
			defer wg.Done()
			defer func() { <-sem }()
			row := ownershipEvaluation{ID: item.ItemID, InputHash: auditInputHash(item), PromptHash: hash, Model: cfg.UGCSlopModel, RequestDate: time.Now().UTC().Format("2006-01-02"), MaxTokens: cfg.LLMMaxTokens, Contract: llm.SlopRequestContract}
			ctx, cancel := context.WithTimeout(context.Background(), 130*time.Second)
			defer cancel()
			baseline, err := client.ProcessUGCItem(ctx, item.Content, item.Notes, false)
			var result *llm.SlopEvaluation
			if err == nil {
				row.Baseline = &baseline.Result.ExtractResult
				row.BaselineBlocked = baseline.Result.Discard || (baseline.Result.IsSlop != nil && *baseline.Result.IsSlop == 1)
				result, err = client.ProcessUGCItem(ctx, item.Content, item.Notes, true)
			}
			if err != nil {
				row.Error = err.Error()
			} else {
				row.Output = &result.Result.ExtractResult
				row.Output.Summary, _ = ownershipProjection(item, "ugc")
				row.Blocked = result.Result.Discard || (result.Result.IsSlop != nil && *result.Result.IsSlop == 1)
				if row.Blocked {
					row.Output.Quality = 0
				}
			}
			mu.Lock()
			cached[item.ItemID] = row
			save(path, cached)
			fmt.Printf("UGC metadata completed=%d error=%t\n", len(cached), err != nil)
			mu.Unlock()
		}(item)
	}
	wg.Wait()
	for _, row := range cached {
		if row.Error != "" {
			panic("metadata provider failures remain; do not accept the release gate")
		}
	}
}

func loadOwnershipEvaluations(dir string) map[int64]ownershipEvaluation {
	rows := map[int64]ownershipEvaluation{}
	b, e := os.ReadFile(filepath.Join(dir, "ownership-evaluations.json"))
	must(e)
	must(json.Unmarshal(b, &rows))
	return rows
}
