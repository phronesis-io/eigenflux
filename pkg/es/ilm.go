package es

import (
	"bytes"
	"context"
	"eigenflux_server/pkg/logger"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
)

const (
	// IndexName is the write alias pointing to the current hot index, automatically switches after Rollover
	IndexName = "items"
	// ReadIndexPattern is used to read across all backing indices after Rollover
	ReadIndexPattern = "items-*"

	ILMPolicyName     = "items-policy"
	IndexTemplateName = "items-template"
	InitialIndexName  = "items-000001"
)

// ilmPolicy defines the Hot→Warm→Cold three-phase lifecycle strategy
var ilmPolicy = map[string]interface{}{
	"policy": map[string]interface{}{
		"phases": map[string]interface{}{
			"hot": map[string]interface{}{
				"actions": map[string]interface{}{
					"rollover": map[string]interface{}{
						"max_age":  "7d",
						"max_size": "20gb",
					},
					"set_priority": map[string]interface{}{
						"priority": 100,
					},
				},
			},
			"warm": map[string]interface{}{
				"min_age": "7d",
				"actions": map[string]interface{}{
					"forcemerge": map[string]interface{}{
						"max_num_segments": 1,
					},
					"allocate": map[string]interface{}{
						"number_of_replicas": 0,
					},
					"readonly":     map[string]interface{}{},
					"set_priority": map[string]interface{}{"priority": 50},
				},
			},
			"cold": map[string]interface{}{
				"min_age": "90d",
				"actions": map[string]interface{}{
					"allocate": map[string]interface{}{
						"number_of_replicas": 0,
					},
					"readonly":     map[string]interface{}{},
					"set_priority": map[string]interface{}{"priority": 0},
				},
			},
		},
	},
}

// buildIndexTemplate dynamically builds composable index template using environment variables
func buildIndexTemplate(embeddingDims int) map[string]interface{} {
	// Read configuration from environment variables, defaults: shards=1, replicas=0 (suitable for single node)
	shards := getEnvInt("ES_SHARDS", 1)
	replicas := getEnvInt("ES_REPLICAS", 0)

	return map[string]interface{}{
		"index_patterns": []string{"items-*"},
		"template": map[string]interface{}{
			"settings": map[string]interface{}{
				"number_of_shards":               shards,
				"number_of_replicas":             replicas,
				"refresh_interval":               "30s",
				"index.lifecycle.name":           ILMPolicyName,
				"index.lifecycle.rollover_alias": IndexName,
				"analysis": map[string]interface{}{
					"analyzer": map[string]interface{}{
						"keyword_analyzer": map[string]interface{}{
							"type":      "custom",
							"tokenizer": "keyword",
							"filter":    []string{"lowercase"},
						},
					},
					"normalizer": map[string]interface{}{
						"lowercase_normalizer": map[string]interface{}{
							"type":   "custom",
							"filter": []string{"lowercase"},
						},
					},
				},
			},
			"mappings": BuildIndexMapping(embeddingDims),
		},
	}
}

// getEnvInt reads integer value from environment variable, returns default value on failure
func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return fallback
}

// SetupILM idempotently creates/updates ILM policy, index template, and bootstraps initial index on first run
func SetupILM(ctx context.Context, embeddingDims int) error {
	if err := upsertILMPolicy(ctx); err != nil {
		return fmt.Errorf("upsert ILM policy: %w", err)
	}
	if err := upsertIndexTemplate(ctx, embeddingDims); err != nil {
		return fmt.Errorf("upsert index template: %w", err)
	}
	if err := bootstrapIfNeeded(ctx); err != nil {
		return fmt.Errorf("bootstrap index: %w", err)
	}
	return nil
}

// upsertILMPolicy creates or updates ILM policy (idempotent)
func upsertILMPolicy(ctx context.Context) error {
	body, err := json.Marshal(ilmPolicy)
	if err != nil {
		return fmt.Errorf("marshal ILM policy: %w", err)
	}

	res, err := Client.ILM.PutLifecycle(
		ILMPolicyName,
		Client.ILM.PutLifecycle.WithContext(ctx),
		Client.ILM.PutLifecycle.WithBody(bytes.NewReader(body)),
	)
	if err != nil {
		return fmt.Errorf("put ILM policy: %w", err)
	}
	defer res.Body.Close()

	if res.IsError() {
		return fmt.Errorf("ILM policy error: %s", res.String())
	}

	logger.Default().Info("ILM policy upserted", "policy", ILMPolicyName)
	return nil
}

// upsertIndexTemplate creates or updates composable index template (idempotent)
func upsertIndexTemplate(ctx context.Context, embeddingDims int) error {
	template := buildIndexTemplate(embeddingDims)
	body, err := json.Marshal(template)
	if err != nil {
		return fmt.Errorf("marshal index template: %w", err)
	}

	res, err := Client.Indices.PutIndexTemplate(
		IndexTemplateName,
		bytes.NewReader(body),
		Client.Indices.PutIndexTemplate.WithContext(ctx),
	)
	if err != nil {
		return fmt.Errorf("put index template: %w", err)
	}
	defer res.Body.Close()

	if res.IsError() {
		return fmt.Errorf("index template error: %s", res.String())
	}

	// Log the configuration used
	settings := template["template"].(map[string]interface{})["settings"].(map[string]interface{})
	logger.Default().Info("index template upserted", "template", IndexTemplateName, "shards", settings["number_of_shards"], "replicas", settings["number_of_replicas"])
	return nil
}

// bootstrapIfNeeded creates initial backing index only when write alias does not exist
func bootstrapIfNeeded(ctx context.Context) error {
	// Check if "items" alias exists
	aliasRes, err := Client.Indices.GetAlias(
		Client.Indices.GetAlias.WithContext(ctx),
		Client.Indices.GetAlias.WithName(IndexName),
	)
	if err != nil {
		return fmt.Errorf("check alias: %w", err)
	}
	defer aliasRes.Body.Close()

	if aliasRes.StatusCode == 200 {
		logger.Default().Info("write alias already exists, skipping bootstrap", "alias", IndexName)
		return nil
	}

	// Check if "items" is an old regular index (not an alias)
	idxRes, err := Client.Indices.Exists([]string{IndexName})
	if err != nil {
		return fmt.Errorf("check index existence: %w", err)
	}
	defer idxRes.Body.Close()

	if idxRes.StatusCode == 200 {
		logger.Default().Warn("index exists as a regular index (not alias), skipping bootstrap to avoid data loss",
			"index", IndexName, "action", fmt.Sprintf("DELETE /%s then restart", IndexName))
		return nil
	}

	// Check if initial backing index already exists (enhanced idempotency)
	initialIdxRes, err := Client.Indices.Exists([]string{InitialIndexName})
	if err != nil {
		return fmt.Errorf("check initial index existence: %w", err)
	}
	defer initialIdxRes.Body.Close()

	if initialIdxRes.StatusCode == 200 {
		logger.Default().Info("initial index already exists but alias not bound, skipping bootstrap", "index", InitialIndexName)
		return nil
	}

	// Create initial backing index and bind write alias
	initialMapping := map[string]interface{}{
		"aliases": map[string]interface{}{
			IndexName: map[string]interface{}{
				"is_write_index": true,
			},
		},
	}

	body, err := json.Marshal(initialMapping)
	if err != nil {
		return fmt.Errorf("marshal initial index config: %w", err)
	}

	createRes, err := Client.Indices.Create(
		InitialIndexName,
		Client.Indices.Create.WithContext(ctx),
		Client.Indices.Create.WithBody(bytes.NewReader(body)),
	)
	if err != nil {
		return fmt.Errorf("create initial index: %w", err)
	}
	defer createRes.Body.Close()

	if createRes.IsError() {
		raw, readErr := io.ReadAll(createRes.Body)
		if readErr != nil {
			return fmt.Errorf("read create initial index error body: %w", readErr)
		}
		if isAlreadyExistsCreateError(createRes.StatusCode, raw) {
			logger.Default().Info("initial index already exists (likely concurrent bootstrap), continuing", "index", InitialIndexName)
			return nil
		}
		msg := strings.TrimSpace(string(raw))
		return fmt.Errorf("create initial index error: [%d %s] %s",
			createRes.StatusCode, http.StatusText(createRes.StatusCode), msg)
	}

	logger.Default().Info("ILM bootstrap: created initial index with write alias", "index", InitialIndexName, "alias", IndexName)
	return nil
}

func isAlreadyExistsCreateError(statusCode int, body []byte) bool {
	if statusCode != http.StatusBadRequest && statusCode != http.StatusConflict {
		return false
	}
	text := strings.ToLower(string(body))
	return strings.Contains(text, "resource_already_exists_exception") ||
		strings.Contains(text, "already_exists_exception")
}
