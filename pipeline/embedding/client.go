package embedding

import (
	"bytes"
	"context"
	"eigenflux_server/pkg/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"eigenflux_server/pkg/embeddingmeta"
)

type Provider string

const (
	ProviderOpenAI Provider = "openai"
	ProviderOllama Provider = "ollama"
)

type Client struct {
	provider             Provider
	apiKey               string
	baseURL              string
	model                string
	dimensions           int
	configuredDimensions int
	httpClient           *http.Client
}

// OpenAI API structures
type EmbeddingRequest struct {
	Input          string `json:"input"`
	Model          string `json:"model"`
	Dimensions     int    `json:"dimensions,omitempty"`
	EncodingFormat string `json:"encoding_format,omitempty"`
}

type EmbeddingResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Model string `json:"model"`
	Usage struct {
		PromptTokens int `json:"prompt_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
}

// Ollama API structures
type OllamaEmbeddingRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

type OllamaEmbeddingResponse struct {
	Embedding []float32 `json:"embedding"`
}

func NewClient(provider, apiKey, baseURL, model string, configuredDimensions int) *Client {
	resolvedProvider := embeddingmeta.NormalizeProvider(provider)
	resolvedModel := embeddingmeta.ResolveModel(resolvedProvider, model)
	dimensions, _ := embeddingmeta.ResolveDimensions(resolvedProvider, resolvedModel, configuredDimensions)

	var prov Provider
	if resolvedProvider == embeddingmeta.ProviderOllama {
		prov = ProviderOllama
		if baseURL == "" {
			baseURL = "http://localhost:11434"
		}
	} else {
		prov = ProviderOpenAI
		if baseURL == "" {
			baseURL = "https://api.openai.com/v1"
		}
	}

	return &Client{
		provider:             prov,
		apiKey:               apiKey,
		baseURL:              baseURL,
		model:                resolvedModel,
		dimensions:           dimensions,
		configuredDimensions: configuredDimensions,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (c *Client) Dimensions() int {
	if c == nil {
		return 0
	}
	return c.dimensions
}

func (c *Client) GetEmbedding(ctx context.Context, text string) ([]float32, error) {
	switch c.provider {
	case ProviderOllama:
		return c.getOllamaEmbedding(ctx, text)
	case ProviderOpenAI:
		return c.getOpenAIEmbedding(ctx, text)
	default:
		return nil, fmt.Errorf("unsupported provider: %s", c.provider)
	}
}

func (c *Client) getOpenAIEmbedding(ctx context.Context, text string) ([]float32, error) {
	reqBody := EmbeddingRequest{
		Input: text,
		Model: c.model,
	}
	if c.configuredDimensions > 0 {
		reqBody.Dimensions = c.configuredDimensions
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/embeddings", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}

	var embResp EmbeddingResponse
	if err := json.NewDecoder(resp.Body).Decode(&embResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	if len(embResp.Data) == 0 {
		return nil, fmt.Errorf("no embedding data in response")
	}

	return embResp.Data[0].Embedding, nil
}

func (c *Client) getOllamaEmbedding(ctx context.Context, text string) ([]float32, error) {
	reqBody := OllamaEmbeddingRequest{
		Model:  c.model,
		Prompt: text,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/embeddings", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}

	var embResp OllamaEmbeddingResponse
	if err := json.NewDecoder(resp.Body).Decode(&embResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	if len(embResp.Embedding) == 0 {
		return nil, fmt.Errorf("no embedding data in response")
	}

	return embResp.Embedding, nil
}
