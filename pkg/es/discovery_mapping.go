package es

import (
	"bytes"
	"context"
	searchindex "eigenflux_server/rpc/sort/discovery/index"
	"encoding/json"
	"fmt"
)

// EnsureRetrievalSlots upgrades existing backing indices before any projector
// writes canonical IDs, preventing dynamic text mappings on legacy indices.
func EnsureRetrievalSlots(ctx context.Context, indices ...string) error {
	if Client == nil {
		return fmt.Errorf("Elasticsearch client unavailable")
	}
	body, err := json.Marshal(map[string]any{"properties": map[string]any{"retrieval_slots": searchindex.SlotsMapping()}})
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
		r.Body.Close()
		if status >= 300 {
			return fmt.Errorf("discovery slot mapping for %s failed: HTTP %d", index, status)
		}
	}
	return nil
}
