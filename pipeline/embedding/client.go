package embedding

import (
	"bytes"
	"context"
	"eigenflux_server/pkg/json"
	"errors"
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

const maxEmbeddingResponseBytes int64 = 1 << 20

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

func (c *Client) Model() string {
	return c.model
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
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxEmbeddingResponseBytes+1))
		return nil, fmt.Errorf("API error %d", resp.StatusCode)
	}

	var embResp EmbeddingResponse
	if err := decodeBoundedEmbeddingResponse(resp.Body, &embResp); err != nil {
		return nil, err
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
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxEmbeddingResponseBytes+1))
		return nil, fmt.Errorf("API error %d", resp.StatusCode)
	}

	var embResp OllamaEmbeddingResponse
	if err := decodeBoundedEmbeddingResponse(resp.Body, &embResp); err != nil {
		return nil, err
	}

	if len(embResp.Embedding) == 0 {
		return nil, fmt.Errorf("no embedding data in response")
	}

	return embResp.Embedding, nil
}

func decodeBoundedEmbeddingResponse(body io.Reader, destination any) error {
	limited := &io.LimitedReader{R: body, N: maxEmbeddingResponseBytes + 1}
	decoder := json.NewDecoder(limited)
	if err := decoder.Decode(destination); err != nil || limited.N == 0 {
		return fmt.Errorf("decode response")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("decode response")
	}
	return nil
}
