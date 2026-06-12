package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// SubIntent is one decomposed unit of an open-ended task. Produced by
// DecomposeTask when an agent did not supply its own sub_intents to
// SearchServices.
type SubIntent struct {
	Name       string  `json:"name"`
	QueryText  string  `json:"query_text"`
	Importance float64 `json:"importance"`
}

// Chat is the minimal LLM surface DecomposeTask depends on. *Client satisfies
// this via its exported Call method.
type Chat interface {
	Complete(ctx context.Context, prompt string) (string, error)
}

// chatAdapter lets *Client satisfy Chat without exposing PromptRegistry.
type chatAdapter struct{ c *Client }

func (a *chatAdapter) Complete(ctx context.Context, prompt string) (string, error) {
	return a.c.Call(ctx, prompt, "task_decompose")
}

// AsChat returns a Chat wrapper around *Client tagging metrics as
// "task_decompose". Used by the sort handler when an agent did not provide
// pre-decomposed sub-intents.
func (c *Client) AsChat() Chat {
	return &chatAdapter{c: c}
}

// DecomposeTask asks the LLM to split rawQuery into 2-6 sub-intents.
// The caller is responsible for length cap + trim before consuming.
func DecomposeTask(ctx context.Context, chat Chat, rawQuery string) ([]SubIntent, error) {
	if strings.TrimSpace(rawQuery) == "" {
		return nil, fmt.Errorf("decompose: empty query")
	}
	prompt := buildDecomposePrompt(rawQuery)
	resp, err := chat.Complete(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("decompose llm: %w", err)
	}
	cleaned := stripFence(strings.TrimSpace(resp))
	var parsed struct {
		SubIntents []SubIntent `json:"sub_intents"`
	}
	if err := json.Unmarshal([]byte(cleaned), &parsed); err != nil {
		return nil, fmt.Errorf("decompose parse: %w", err)
	}
	if len(parsed.SubIntents) == 0 {
		return nil, fmt.Errorf("decompose: no sub-intents in response")
	}
	return parsed.SubIntents, nil
}

func buildDecomposePrompt(q string) string {
	return `You are decomposing an open-ended user task into 2-6 sub-intents for a search engine.
Each sub-intent describes ONE capability needed to accomplish part of the task.
Return ONLY JSON, no prose.

Schema:
{ "sub_intents": [{"name":"<short label>","query_text":"<search-friendly phrasing>","importance":<float 0-1>}] }

Task:
` + q
}

func stripFence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}
