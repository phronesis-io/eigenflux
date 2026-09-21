package agentindex

import (
	"bytes"
	"context"
	"eigenflux_server/pkg/discovery"
	"eigenflux_server/pkg/es"
	"encoding/json"
	"fmt"
	"gorm.io/gorm"
	"strconv"
)

func Ensure(ctx context.Context, index string, dims int) error {
	if index == "" || dims < 1 {
		return fmt.Errorf("invalid Agent index configuration")
	}
	properties := map[string]any{"agent_id": map[string]any{"type": "long"}, "version": map[string]any{"type": "long"}, "projection_version": map[string]any{"type": "long"}, "active": map[string]any{"type": "boolean"}, "search_text": map[string]any{"type": "text"}, "display_name": map[string]any{"type": "text"}, "retrieval_slots": discovery.SlotsMapping(), "activity_at": map[string]any{"type": "long"}, "updated_at": map[string]any{"type": "long"}, "embedding": map[string]any{"type": "dense_vector", "dims": dims, "index": true, "similarity": "cosine"}}
	resp, err := es.Client.Indices.Exists([]string{index}, es.Client.Indices.Exists.WithContext(ctx))
	if err != nil {
		return err
	}
	status := resp.StatusCode
	resp.Body.Close()
	if status == 200 {
		body, err := json.Marshal(map[string]any{"properties": properties})
		if err != nil {
			return err
		}
		update, err := es.Client.Indices.PutMapping([]string{index}, bytes.NewReader(body), es.Client.Indices.PutMapping.WithContext(ctx))
		if err != nil {
			return err
		}
		defer update.Body.Close()
		if update.IsError() {
			return fmt.Errorf("Agent index mapping incompatible: %d", update.StatusCode)
		}
		return nil
	}
	if status != 404 {
		return fmt.Errorf("Agent index check failed: %d", status)
	}
	body, err := json.Marshal(map[string]any{"mappings": map[string]any{"dynamic": "strict", "properties": properties}})
	if err != nil {
		return err
	}
	resp, err = es.Client.Indices.Create(index, es.Client.Indices.Create.WithContext(ctx), es.Client.Indices.Create.WithBody(bytes.NewReader(body)))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.IsError() {
		return fmt.Errorf("Agent index create failed: %d", resp.StatusCode)
	}
	return nil
}

type Projector struct {
	DB       *gorm.DB
	Index    string
	Embedder discovery.Embedder
}

func (p Projector) Project(ctx context.Context, id int64) error {
	// Allocate before the read: a concurrent later Card rebuild receives a larger
	// fence, so a deletion tombstone cannot overwrite a newer recreated Card.
	var fence int64
	if err := p.DB.WithContext(ctx).Raw("SELECT nextval('agent_card_rebuild_fence_seq')").Scan(&fence).Error; err != nil {
		return err
	}
	rows, err := Load(ctx, p.DB, []int64{id})
	if err != nil {
		return err
	}
	d := Document{AgentID: id, Version: fence, ProjectionVersion: fence, Active: false}
	if len(rows) > 0 {
		d = rows[0]
		if d.ProjectionVersion <= 0 {
			return fmt.Errorf("unfenced Agent Card")
		}
		if d.Active && d.SearchText != "" {
			if p.Embedder == nil {
				return fmt.Errorf("Agent embedder unavailable")
			}
			d.Embedding, err = p.Embedder.GetEmbedding(ctx, d.SearchText)
			if err != nil {
				return err
			}
		}
	}
	b, err := json.Marshal(d)
	if err != nil {
		return err
	}
	resp, err := es.Client.Index(p.Index, bytes.NewReader(b), es.Client.Index.WithContext(ctx), es.Client.Index.WithDocumentID(strconv.FormatInt(id, 10)), es.Client.Index.WithVersion(int(d.ProjectionVersion)), es.Client.Index.WithVersionType("external_gte"))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 409 {
		return nil
	}
	if resp.IsError() {
		return fmt.Errorf("Agent projection failed: %d", resp.StatusCode)
	}
	return nil
}
