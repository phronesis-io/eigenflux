// Package services owns the decision engine boundary.
//
// Per design/game-brief.md, model invocation is "Per-platform direct
// connection / Fully self-built": the Hub is a central match service but never
// proxies model calls. Each client connects to its provider directly and only
// submits the resulting decision to the Hub. This package therefore defines
// the adapter contract plus the local rule-engine implementation selected for
// MVP phase 1 (no API key, no cost, fully reproducible).
package services

import (
	"fmt"
	"sort"
	"strings"

	"fluxspace.ai/hub/models"
	"fluxspace.ai/hub/referee"
)

// ModelAdapter is the GDD's unified interface. Provider adapters (Claude,
// OpenAI, local runtime) implement it on the client side, not on the Hub.
type ModelAdapter interface {
	Decide(ctx referee.DecisionContext) (models.Decision, error)
	Discuss(ctx referee.DecisionContext) (string, error)
	Reflect(companionID string, events []string) ([]models.Memory, error)
}

// RuleEngineAdapter is the deterministic fallback that doubles as the MVP
// phase-1 decision source. It performs no network calls and no token spend.
type RuleEngineAdapter struct{}

func NewRuleEngineAdapter() *RuleEngineAdapter { return &RuleEngineAdapter{} }

func (a *RuleEngineAdapter) Decide(ctx referee.DecisionContext) (models.Decision, error) {
	result, err := referee.DecideAction(ctx)
	if err != nil {
		return models.Decision{}, err
	}
	top := result.Scores[0].Breakdown
	keys := make([]string, 0, len(top))
	for k := range top {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return abs(top[keys[i]]) > abs(top[keys[j]])
	})
	summary := fmt.Sprintf("Selected %s; dominant factors: %s (%.2f), %s (%.2f)",
		result.Action.Label, keys[0], top[keys[0]], keys[1], top[keys[1]])
	return models.Decision{
		ActionID:         result.Action.ID,
		ReasoningSummary: summary,
		Confidence:       result.Confidence,
		Source:           "rule_engine",
	}, nil
}

// Discuss produces one bounded discussion line from the same scoring evidence.
// The 200-character cap is enforced by the Hub's discussion manager (GDD).
func (a *RuleEngineAdapter) Discuss(ctx referee.DecisionContext) (string, error) {
	result, err := referee.DecideAction(ctx)
	if err != nil {
		return "", err
	}
	line := fmt.Sprintf("I lean toward %s (confidence %.0f).", result.Action.Label, result.Confidence)
	if len([]rune(line)) > 200 {
		line = string([]rune(line)[:200])
	}
	return line, nil
}

// Reflect implements bounded post-match reflection: 1-3 memories, 500 token
// ceiling (approximated by characters here), source-tagged to the match.
// The 30-minute cooldown is enforced by the caller, not by this adapter.
func (a *RuleEngineAdapter) Reflect(companionID string, events []string) ([]models.Memory, error) {
	if companionID == "" {
		return nil, fmt.Errorf("companion id is required")
	}
	if len(events) == 0 {
		return []models.Memory{}, nil
	}
	count := len(events)
	if count > 3 {
		count = 3
	}
	out := make([]models.Memory, 0, count)
	budget := 500
	for i := 0; i < count; i++ {
		text := strings.TrimSpace(events[i])
		if text == "" {
			continue
		}
		if len([]rune(text)) > budget {
			text = string([]rune(text)[:budget])
		}
		budget -= len([]rune(text))
		out = append(out, models.Memory{
			ID:          fmt.Sprintf("mem_%s_%d", companionID, i),
			CompanionID: companionID,
			Kind:        models.MemoryPersonalReflection,
			Content:     text,
			Source:      models.MemorySource{Kind: "match_end", RefID: companionID, Round: 0},
			Strength:    1,
		})
		if budget <= 0 {
			break
		}
	}
	return out, nil
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
