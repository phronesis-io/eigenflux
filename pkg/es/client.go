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

// InitES initializes the Elasticsearch client
func InitES(expectedEmbeddingDims int) error {
	if expectedEmbeddingDims <= 0 {
		return fmt.Errorf("embedding dimensions are not configured; set EMBEDDING_DIMENSIONS or use a known EMBEDDING_MODEL")
	}

	esURL := os.Getenv("ES_URL")
	if esURL == "" {
		esPort := strings.TrimSpace(os.Getenv("ELASTICSEARCH_HTTP_PORT"))
		if esPort == "" {
			esPort = "9200"
		}
		esURL = "http://localhost:" + esPort
	}

	cfg := elasticsearch.Config{
		Addresses: []string{esURL},
		Username:  strings.TrimSpace(os.Getenv("ES_USERNAME")),
		Password:  os.Getenv("ES_PASSWORD"),
		Transport: &http.Transport{
			MaxIdleConnsPerHost:   10,
			ResponseHeaderTimeout: 5 * time.Second,
		},
	}

	var err error
	Client, err = elasticsearch.NewClient(cfg)
	if err != nil {
		return fmt.Errorf("failed to create ES client: %w", err)
	}

	// Test connection
	res, err := Client.Info()
	if err != nil {
		return fmt.Errorf("failed to connect to ES: %w", err)
	}
	defer res.Body.Close()

	if res.IsError() {
		return fmt.Errorf("ES returned error: %s", res.String())
	}

	logger.Default().Info("connected to Elasticsearch successfully")

	// Setup ILM policy, index template, and bootstrap initial index if needed
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
