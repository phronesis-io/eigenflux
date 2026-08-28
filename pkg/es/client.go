package es

import (
	"context"
	"eigenflux_server/pkg/logger"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	elasticsearch "github.com/elastic/go-elasticsearch/v8"
)

var Client *elasticsearch.Client
var embeddingDims int

// InitClient initializes and verifies the Elasticsearch transport without
// creating or changing any index, template, policy, or mapping.
func InitClient() error {
	return initClientWithTransport(nil)
}

func initClientWithTransport(transport http.RoundTripper) error {
	esURL := os.Getenv("ES_URL")
	if esURL == "" {
		esPort := strings.TrimSpace(os.Getenv("ELASTICSEARCH_HTTP_PORT"))
		if esPort == "" {
			esPort = "9200"
		}
		esURL = "http://localhost:" + esPort
	}
	if transport == nil {
		transport = &http.Transport{
			MaxIdleConnsPerHost:   10,
			ResponseHeaderTimeout: 5 * time.Second,
		}
	}
	candidate, err := elasticsearch.NewClient(elasticsearch.Config{
		Addresses: []string{esURL},
		Username:  strings.TrimSpace(os.Getenv("ES_USERNAME")),
		Password:  os.Getenv("ES_PASSWORD"),
		Transport: transport,
	})
	if err != nil {
		return fmt.Errorf("failed to create ES client: %w", err)
	}
	res, err := candidate.Info()
	if err != nil {
		if res != nil && res.Body != nil {
			_ = res.Body.Close()
		}
		return fmt.Errorf("failed to connect to ES: %w", err)
	}
	if res == nil || res.Body == nil {
		return fmt.Errorf("failed to connect to ES")
	}
	defer res.Body.Close()
	if res.IsError() {
		return fmt.Errorf("ES returned status %d", res.StatusCode)
	}
	Client = candidate
	logger.Default().Info("connected to Elasticsearch successfully")
	return nil
}

// InitES initializes Elasticsearch and owns the normal item-index bootstrap.
func InitES(expectedEmbeddingDims int) error {
	return initESWithTransport(expectedEmbeddingDims, nil)
}

func initESWithTransport(expectedEmbeddingDims int, transport http.RoundTripper) error {
	if expectedEmbeddingDims <= 0 {
		return fmt.Errorf("embedding dimensions are not configured; set EMBEDDING_DIMENSIONS or use a known EMBEDDING_MODEL")
	}
	if err := initClientWithTransport(transport); err != nil {
		return err
	}
	if err := SetupILM(context.Background(), expectedEmbeddingDims); err != nil {
		return fmt.Errorf("failed to setup ILM: %w", err)
	}
	if _, err := ValidateReadIndexEmbeddingDimensions(context.Background(), expectedEmbeddingDims); err != nil {
		return fmt.Errorf("failed to validate embedding dimensions: %w", err)
	}
	embeddingDims = expectedEmbeddingDims
	return nil
}

func EmbeddingDimensions() int {
	return embeddingDims
}
