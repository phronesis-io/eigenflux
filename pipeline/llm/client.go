package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"eigenflux_server/pkg/config"
)

type Client struct {
	client openai.Client
	model  string
}

func NewClient(cfg *config.Config) *Client {
	opts := []option.RequestOption{
		option.WithAPIKey(cfg.LLMApiKey),
	}
	opts = append(opts, option.WithBaseURL(normalizeBaseURL(cfg.LLMBaseURL)))
	return &Client{
		client: openai.NewClient(opts...),
		model:  cfg.LLMModel,
	}
}

type ExtractResult struct {
	// Original fields
	Summary          string   `json:"summary"`
	BroadcastType    string   `json:"broadcast_type"`
	Domains          []string `json:"domains"`
	Keywords         []string `json:"keywords"`
	ExpireTime       string   `json:"expire_time"`
	Geo              string   `json:"geo"`
	SourceType       string   `json:"source_type"`
	ExpectedResponse string   `json:"expected_response"`
	GroupID          string   `json:"group_id"`

	// New fields for quality filtering
	Discard       bool    `json:"discard"`
	DiscardReason string  `json:"discard_reason"`
	Lang          string  `json:"lang"`
	Quality       float64 `json:"quality"`
	Timeliness    string  `json:"timeliness"`
}

// ExtractKeywords extracts 3-10 keywords and country from an agent's bio
func (c *Client) ExtractKeywords(ctx context.Context, bio string) ([]string, string, error) {
	prompt := fmt.Sprintf(`Extract 3-10 keywords and the country from the following agent bio. Keywords should represent the user's interests and topics they care about. Return ONLY a JSON object with "keywords" (array of strings) and "country" (ISO code string, empty if not mentioned). Please return in English words.
Bio: %s

Response format: {"keywords": ["keyword1", "keyword2", "keyword3"], "country": "CN"}`, bio)

	resp, err := c.call(ctx, prompt)
	if err != nil {
		return nil, "", fmt.Errorf("LLM call failed: %w", err)
	}

	var result struct {
		Keywords []string `json:"keywords"`
		Country  string   `json:"country"`
	}
	if err := json.Unmarshal([]byte(resp), &result); err != nil {
		return nil, "", fmt.Errorf("failed to parse keywords JSON: %w, raw: %s", err, resp)
	}
	return result.Keywords, result.Country, nil
}

// ProcessItem generates structured information for a content item
func (c *Client) ProcessItem(ctx context.Context, rawContent, rawNotes string) (*ExtractResult, error) {
	prompt := fmt.Sprintf(ProcessItemPrompts, rawContent, rawNotes)

	resp, err := c.call(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("LLM call failed: %w", err)
	}

	var result ExtractResult
	if err := json.Unmarshal([]byte(resp), &result); err != nil {
		return nil, fmt.Errorf("failed to parse result JSON: %w, raw: %s", err, resp)
	}
	return &result, nil
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
	// Try to find JSON in code blocks
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
	// Find matching end
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
