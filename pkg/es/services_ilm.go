package es

import (
	"bytes"
	"context"
	"eigenflux_server/pkg/json"
	"eigenflux_server/pkg/logger"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const (
	ServicesIndexName    = "services"
	ServicesReadPattern  = "services-*"
	ServicesILMPolicy    = "services-policy"
	ServicesTemplateName = "services-template"
	ServicesInitialIndex = "services-000001"
)

// servicesILMPolicy is intentionally separate from the items ilmPolicy.
// Service declarations are long-lived metadata, not a continuous event stream:
// once published, a service remains discoverable until the seller takes it
// offline, and old services are not necessarily stale (they may receive a new
// version via UpdateService at any time). The policy therefore:
//   - rolls over only on hard size/age limits (50gb / 365d) rather than the
//     items defaults (20gb / 7d), so most deployments stay on a single shard,
//   - does NOT transition to warm/cold/readonly — services stay hot and
//     fully searchable indefinitely so updates land in-place,
//   - has no delete phase — offline services are filtered at query time, not
//     evicted from the index.
var servicesILMPolicy = map[string]interface{}{
	"policy": map[string]interface{}{
		"phases": map[string]interface{}{
			"hot": map[string]interface{}{
				"actions": map[string]interface{}{
					"rollover": map[string]interface{}{
						"max_age":  "365d",
						"max_size": "50gb",
					},
					"set_priority": map[string]interface{}{
						"priority": 100,
					},
				},
			},
		},
	},
}

func buildServicesIndexTemplate(embeddingDims int) map[string]interface{} {
	shards := getEnvInt("ES_SHARDS", 1)
	replicas := getEnvInt("ES_REPLICAS", 0)

	return map[string]interface{}{
		"index_patterns": []string{"services-*"},
		"template": map[string]interface{}{
			"settings": map[string]interface{}{
				"number_of_shards":               shards,
				"number_of_replicas":             replicas,
				"refresh_interval":               "30s",
				"index.lifecycle.name":           ServicesILMPolicy,
				"index.lifecycle.rollover_alias": ServicesIndexName,
				"analysis": map[string]interface{}{
					"normalizer": map[string]interface{}{
						"lowercase_normalizer": map[string]interface{}{
							"type":   "custom",
							"filter": []string{"lowercase"},
						},
					},
				},
			},
			"mappings": BuildServicesMapping(embeddingDims),
		},
	}
}

// SetupServicesILM idempotently creates/updates ILM policy, index template, and bootstraps initial index on first run
func SetupServicesILM(ctx context.Context, embeddingDims int) error {
	if err := upsertServicesILMPolicy(ctx); err != nil {
		return fmt.Errorf("upsert services ILM policy: %w", err)
	}
	if err := upsertServicesTemplate(ctx, embeddingDims); err != nil {
		return fmt.Errorf("upsert services index template: %w", err)
	}
	if err := bootstrapServicesIfNeeded(ctx); err != nil {
		return fmt.Errorf("bootstrap services index: %w", err)
	}
	return nil
}

func upsertServicesILMPolicy(ctx context.Context) error {
	body, err := json.Marshal(servicesILMPolicy)
	if err != nil {
		return fmt.Errorf("marshal services ILM policy: %w", err)
	}

	res, err := Client.ILM.PutLifecycle(
		ServicesILMPolicy,
		Client.ILM.PutLifecycle.WithContext(ctx),
		Client.ILM.PutLifecycle.WithBody(bytes.NewReader(body)),
	)
	if err != nil {
		return fmt.Errorf("put services ILM policy: %w", err)
	}
	defer res.Body.Close()

	if res.IsError() {
		return fmt.Errorf("services ILM policy error: %s", res.String())
	}

	logger.Default().Info("services ILM policy upserted", "policy", ServicesILMPolicy)
	return nil
}

func upsertServicesTemplate(ctx context.Context, embeddingDims int) error {
	template := buildServicesIndexTemplate(embeddingDims)
	body, err := json.Marshal(template)
	if err != nil {
		return fmt.Errorf("marshal services index template: %w", err)
	}

	res, err := Client.Indices.PutIndexTemplate(
		ServicesTemplateName,
		bytes.NewReader(body),
		Client.Indices.PutIndexTemplate.WithContext(ctx),
	)
	if err != nil {
		return fmt.Errorf("put services index template: %w", err)
	}
	defer res.Body.Close()

	if res.IsError() {
		return fmt.Errorf("services index template error: %s", res.String())
	}

	settings := template["template"].(map[string]interface{})["settings"].(map[string]interface{})
	logger.Default().Info("services index template upserted", "template", ServicesTemplateName, "shards", settings["number_of_shards"], "replicas", settings["number_of_replicas"])
	return nil
}

func bootstrapServicesIfNeeded(ctx context.Context) error {
	aliasRes, err := Client.Indices.GetAlias(
		Client.Indices.GetAlias.WithContext(ctx),
		Client.Indices.GetAlias.WithName(ServicesIndexName),
	)
	if err != nil {
		return fmt.Errorf("check services alias: %w", err)
	}
	defer aliasRes.Body.Close()

	if aliasRes.StatusCode == 200 {
		logger.Default().Info("services write alias already exists, skipping bootstrap", "alias", ServicesIndexName)
		return nil
	}

	idxRes, err := Client.Indices.Exists([]string{ServicesIndexName})
	if err != nil {
		return fmt.Errorf("check services index existence: %w", err)
	}
	defer idxRes.Body.Close()

	if idxRes.StatusCode == 200 {
		logger.Default().Warn("services index exists as a regular index (not alias), skipping bootstrap to avoid data loss",
			"index", ServicesIndexName, "action", fmt.Sprintf("DELETE /%s then restart", ServicesIndexName))
		return nil
	}

	initialIdxRes, err := Client.Indices.Exists([]string{ServicesInitialIndex})
	if err != nil {
		return fmt.Errorf("check services initial index existence: %w", err)
	}
	defer initialIdxRes.Body.Close()

	if initialIdxRes.StatusCode == 200 {
		logger.Default().Info("services initial index already exists but alias not bound, skipping bootstrap", "index", ServicesInitialIndex)
		return nil
	}

	initialMapping := map[string]interface{}{
		"aliases": map[string]interface{}{
			ServicesIndexName: map[string]interface{}{
				"is_write_index": true,
			},
		},
	}

	body, err := json.Marshal(initialMapping)
	if err != nil {
		return fmt.Errorf("marshal services initial index config: %w", err)
	}

	createRes, err := Client.Indices.Create(
		ServicesInitialIndex,
		Client.Indices.Create.WithContext(ctx),
		Client.Indices.Create.WithBody(bytes.NewReader(body)),
	)
	if err != nil {
		return fmt.Errorf("create services initial index: %w", err)
	}
	defer createRes.Body.Close()

	if createRes.IsError() {
		raw, readErr := io.ReadAll(createRes.Body)
		if readErr != nil {
			return fmt.Errorf("read create services initial index error body: %w", readErr)
		}
		if isAlreadyExistsCreateError(createRes.StatusCode, raw) {
			logger.Default().Info("services initial index already exists (likely concurrent bootstrap), continuing", "index", ServicesInitialIndex)
			return nil
		}
		msg := strings.TrimSpace(string(raw))
		return fmt.Errorf("create services initial index error: [%d %s] %s",
			createRes.StatusCode, http.StatusText(createRes.StatusCode), msg)
	}

	logger.Default().Info("services ILM bootstrap: created initial index with write alias", "index", ServicesInitialIndex, "alias", ServicesIndexName)
	return nil
}
