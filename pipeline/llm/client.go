package llm

import (
	"context"
	"fmt"
	"strings"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"eigenflux_server/pkg/config"
)

type Client struct {
	client  openai.Client
	model   string
	prompts *PromptRegistry
}

func NewClient(cfg *config.Config, prompts *PromptRegistry) *Client {
	opts := []option.RequestOption{
		option.WithAPIKey(cfg.LLMApiKey),
	}
	opts = append(opts, option.WithBaseURL(normalizeBaseURL(cfg.LLMBaseURL)))
	return &Client{
		client:  openai.NewClient(opts...),
		model:   cfg.LLMModel,
		prompts: prompts,
	}
}

type ExtractResult struct {
	Summary          string   `json:"summary"`
	BroadcastType    string   `json:"broadcast_type"`
	Domains          []string `json:"domains"`
	Keywords         []string `json:"keywords"`
	ExpireTime       string   `json:"expire_time"`
	Geo              string   `json:"geo"`
	SourceType       string   `json:"source_type"`
	ExpectedResponse string   `json:"expected_response"`
	GroupID          string   `json:"group_id"`

	Discard       bool    `json:"discard"`
	DiscardReason string  `json:"discard_reason"`
	Lang          string  `json:"lang"`
	Quality       float64 `json:"quality"`
	Timeliness    string  `json:"timeliness"`
}

// SafetyResult holds the output of the safety check prompt.
type SafetyResult struct {
	Safe   bool   `json:"safe"`
	Flag   string `json:"flag"`
	Reason string `json:"reason"`
}

// CheckSafety runs content through the safety filter before processing.
func (c *Client) CheckSafety(ctx context.Context, rawContent, rawNotes string) (*SafetyResult, error) {
	return SafetyPrompt.Execute(ctx, c, SafetyInput{Content: rawContent, Notes: rawNotes})
}

// ExtractKeywords extracts 3-10 keywords and country from an agent's bio
func (c *Client) ExtractKeywords(ctx context.Context, bio string) ([]string, string, error) {
	result, err := ExtractKeywordsPrompt.Execute(ctx, c, ExtractKeywordsInput{Bio: bio})
	if err != nil {
		return nil, "", err
	}
	return result.Keywords, result.Country, nil
}

// ProcessItem generates structured information for a content item
func (c *Client) ProcessItem(ctx context.Context, rawContent, rawNotes string) (*ExtractResult, error) {
	return ProcessItemPrompt.Execute(ctx, c, ProcessItemInput{Content: rawContent, Notes: rawNotes})
}

func (c *Client) call(ctx context.Context, prompt string) (string, error) {
	completion, err := c.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model:               openai.ChatModel(c.model),
		MaxCompletionTokens: openai.Int(1024),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage(prompt),
		},
	})
	if err != nil {
		return "", fmt.Errorf("LLM API error: %w", err)
	}

	if len(completion.Choices) == 0 {
		return "", fmt.Errorf("empty response from LLM")
	}

	text := strings.TrimSpace(completion.Choices[0].Message.Content)
	if text == "" {
		return "", fmt.Errorf("no text content in LLM response")
	}

	text = extractJSON(text)
	return text, nil
}

func normalizeBaseURL(baseURL string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return "https://api.openai.com/v1"
	}
	if strings.HasSuffix(baseURL, "/v1") {
		return baseURL
	}
	return baseURL + "/v1"
}

// extractJSON tries to extract JSON from text that might be wrapped in markdown code blocks
func extractJSON(text string) string {
	start := -1
	for i := 0; i < len(text); i++ {
		if text[i] == '[' || text[i] == '{' {
			start = i
			break
		}
	}
	if start == -1 {
		return text
	}
	end := -1
	openChar := text[start]
	closeChar := byte('}')
	if openChar == '[' {
		closeChar = ']'
	}
	depth := 0
	for i := start; i < len(text); i++ {
		if text[i] == openChar {
			depth++
		} else if text[i] == closeChar {
			depth--
			if depth == 0 {
				end = i + 1
				break
			}
		}
	}
	if end == -1 {
		return text[start:]
	}
	return text[start:end]
}
