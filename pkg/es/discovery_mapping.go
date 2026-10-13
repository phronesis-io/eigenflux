package es

import (
	"bytes"
	"context"
	searchindex "eigenflux_server/rpc/sort/discovery/index"
	"encoding/json"
	"fmt"
	"io"
)

// EnsureBroadcastRetrievalFields installs only fields used by broadcast filters.
// The top-level language uses its exact multi-field. Slot language is not a
// broadcast filter and its existing mapping must remain untouched.
func EnsureBroadcastRetrievalFields(ctx context.Context, indices ...string) error {
	return ensureRetrievalMapping(ctx, map[string]any{
		"lang": broadcastLanguageMapping(),
		"retrieval_slots": map[string]any{"properties": map[string]any{
			"provider_region": map[string]any{"type": "keyword"},
		}},
	}, indices...)
}

// EnsureRetrievalSlots installs the exact slot fields used by Agent/Commission
// filters. Broadcast indices must use EnsureBroadcastRetrievalFields instead.
func EnsureRetrievalSlots(ctx context.Context, indices ...string) error {
	return ensureRetrievalMapping(ctx, map[string]any{"retrieval_slots": searchindex.SlotsMapping()}, indices...)
}

func ensureRetrievalMapping(ctx context.Context, properties map[string]any, indices ...string) error {
	if Client == nil {
		return fmt.Errorf("Elasticsearch client unavailable")
	}
	body, err := json.Marshal(map[string]any{"properties": properties})
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, index := range indices {
		if index == "" || seen[index] {
			continue
		}
		seen[index] = true
		r, err := Client.Indices.PutMapping([]string{index}, bytes.NewReader(body), Client.Indices.PutMapping.WithContext(ctx))
		if err != nil {
			return err
		}
		status := r.StatusCode
		if status >= 300 {
			detail, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
			r.Body.Close()
			return fmt.Errorf("discovery retrieval mapping for %s failed: HTTP %d: %s", index, status, detail)
		}
		r.Body.Close()
	}
	return nil
}
