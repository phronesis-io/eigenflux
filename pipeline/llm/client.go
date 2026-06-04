package llm

import (
	"context"
	"fmt"
	"strings"
	"time"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"

	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/metrics"
)

type Client struct {
	client          openai.Client
	model           string
	maxTokens       int
	reasoningEffort shared.ReasoningEffort
	prompts         *PromptRegistry
}

func NewClient(cfg *config.Config, prompts *PromptRegistry) *Client {
	opts := []option.RequestOption{
		option.WithAPIKey(cfg.LLMApiKey),
	}
	opts = append(opts, option.WithBaseURL(normalizeBaseURL(cfg.LLMBaseURL)))
	return &Client{
		client:          openai.NewClient(opts...),
		model:           cfg.LLMModel,
		maxTokens:       cfg.LLMMaxTokens,
		reasoningEffort: shared.ReasoningEffort(cfg.LLMReasoningEffort),
		prompts:         prompts,
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

// SuggestAction generates an action suggestion for a processed item.
func (c *Client) SuggestAction(ctx context.Context, input SuggestActionInput) (*SuggestActionResult, error) {
	return SuggestActionPrompt.Execute(ctx, c, input)
}

// TranslateToChinese renders the given text into Simplified Chinese for
// display, keeping technical terms, product names and identifiers as-is.
// Uses callRaw: translations may legitimately contain brackets, which
// extractJSON would mangle.
func (c *Client) TranslateToChinese(ctx context.Context, text string) (string, error) {
	prompt := "Translate the following content into Simplified Chinese. Keep technical terms, product names, and code identifiers in their original form. Return ONLY the translation with no preamble.\n\n" + text
	out, err := c.callRaw(ctx, prompt, "translate_zh", "low")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (c *Client) call(ctx context.Context, prompt string, promptName string, effortOverride string) (string, error) {
	text, err := c.callRaw(ctx, prompt, promptName, effortOverride)
	if err != nil {
		return "", err
	}
	return extractJSON(text), nil
}

// callRaw sends the prompt and returns the model's raw text output.
func (c *Client) callRaw(ctx context.Context, prompt string, promptName string, effortOverride string) (string, error) {
	effort := c.reasoningEffort
	if effortOverride != "" {
		effort = shared.ReasoningEffort(effortOverride)
	}
	start := time.Now()
	resp, err := c.client.Responses.New(ctx, responses.ResponseNewParams{
		Model:           shared.ResponsesModel(c.model),
		MaxOutputTokens: openai.Int(int64(c.maxTokens)),
		Input: responses.ResponseNewParamsInputUnion{
			OfString: openai.String(prompt),
		},
		Reasoning: shared.ReasoningParam{
			Effort: effort,
		},
	})
	duration := time.Since(start).Seconds()

	metrics.LLMCallDuration.WithLabelValues(promptName).Observe(duration)

	if err != nil {
		return "", fmt.Errorf("LLM API error: %w", err)
	}

	metrics.LLMCompletionTokens.WithLabelValues(promptName).Observe(float64(resp.Usage.OutputTokens))
	metrics.LLMReasoningTokens.WithLabelValues(promptName).Observe(float64(resp.Usage.OutputTokensDetails.ReasoningTokens))

	text := strings.TrimSpace(resp.OutputText())
	if text == "" {
		return "", fmt.Errorf("no text content in LLM response")
	}

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
