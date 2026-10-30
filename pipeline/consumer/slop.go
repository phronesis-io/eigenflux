package consumer

import (
	"context"
	"eigenflux_server/pipeline/llm"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/metrics"
	itemDal "eigenflux_server/rpc/item/dal"
	"encoding/json"
	"fmt"
)

func (c *ItemConsumer) processUGC(ctx context.Context, raw *itemDal.RawItem) (*llm.ExtractResult, error) {
	itemDB := db.DB.WithContext(ctx)
	saved, err := itemDal.GetSlopEvaluation(itemDB, raw.ItemID)
	if err != nil {
		return nil, err
	}
	if saved == nil {
		evaluation, err := c.ugcSlopClient.ProcessUGCItem(ctx, raw.RawContent, raw.RawNotes)
		if err != nil {
			metrics.UGCSlopAttempts.WithLabelValues(llm.SlopFailureStage(err)).Inc()
			return nil, err
		}
		r := evaluation.Result
		row := &itemDal.ItemSlopEvaluation{ItemID: raw.ItemID, PromptVersion: llm.SlopVersion, Model: evaluation.Model, ContentHash: evaluation.ContentHash, TemplateHash: evaluation.TemplateHash, RequestHash: evaluation.RequestHash, Response: evaluation.Response, Discard: r.Discard}
		if !r.Discard {
			row.IsSlop, row.SlopReason, row.SlopScore = r.IsSlop, r.SlopReason, r.SlopScore
			original, effective := r.Quality, r.Quality
			row.Blocked = *r.IsSlop == 1
			if row.Blocked {
				effective = 0
			}
			row.ModelQuality, row.EffectiveQuality = &original, &effective
		}
		saved, err = itemDal.SaveSlopEvaluation(itemDB, row)
		if err != nil {
			return nil, err
		}
		metrics.UGCSlopAttempts.WithLabelValues("validated").Inc()
	}
	if saved.ContentHash != llm.SlopHash(raw.RawContent+"\x00"+raw.RawNotes) || saved.PromptVersion != llm.SlopVersion {
		return nil, fmt.Errorf("slop checkpoint identity mismatch")
	}
	var result llm.UGCExtractResult
	if err := json.Unmarshal([]byte(saved.Response), &result); err != nil {
		return nil, err
	}
	if err := llm.ValidateSlop(&result); err != nil {
		return nil, err
	}
	if saved.Blocked {
		// Another delivery may have read Processing before the first decision
		// committed. Reapply that decision before acknowledging this attempt.
		if _, err := itemDal.SaveSlopEvaluation(itemDB, saved); err != nil {
			return nil, err
		}
		result.Quality = 0
	}
	return &result.ExtractResult, nil
}
