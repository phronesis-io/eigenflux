package discovery

import (
	"context"
	"encoding/json"

	"eigenflux_server/pkg/featureindex"
	searchindex "eigenflux_server/rpc/sort/discovery/index"
)

// hydratePoolText supplies presentation and optional exclusion evidence for the
// ID-only pools. ES candidates already carry this evidence from initial recall.
// It never writes text into the forward projection or fetches ranking features.
func (s *Source) hydratePoolText(ctx context.Context, recalled, hydrated []Document) error {
	plain, filtered := []int64{}, []int64{}
	for _, d := range recalled {
		if d.Ref.Type != Broadcast || d.Version != "" {
			continue
		}
		if d.NeedExclusionText {
			filtered = append(filtered, d.Ref.ID)
		} else {
			plain = append(plain, d.Ref.ID)
		}
	}
	type display struct {
		ItemID, AuthorID              int64
		Summary, Content, Lang, Slots string
	}
	rows := map[int64]display{}
	if len(plain) > 0 {
		var values []display
		if err := s.DB.WithContext(ctx).Table("processed_items").Select("item_id,summary").Where("item_id IN ?", plain).Scan(&values).Error; err != nil {
			return err
		}
		for _, v := range values {
			rows[v.ItemID] = v
		}
	}
	hashes := map[int64]string{}
	if len(filtered) > 0 {
		var values []display
		if err := s.DB.WithContext(ctx).Raw(`SELECT p.item_id,r.author_agent_id AS author_id,r.raw_content AS content,p.summary,p.lang,p.retrieval_slots::text AS slots
   FROM processed_items p JOIN raw_items r USING(item_id) WHERE p.item_id IN ?`, filtered).Scan(&values).Error; err != nil {
			return err
		}
		for _, v := range values {
			var slots searchindex.Slots
			if err := json.Unmarshal([]byte(v.Slots), &slots); err != nil {
				return err
			}
			if v.Lang != "" {
				slots.Lang = []string{v.Lang}
			}
			hashes[v.ItemID] = featureindex.BroadcastContentHash(v.ItemID, v.AuthorID, v.Content+"\n"+v.Summary, slots)
			rows[v.ItemID] = v
		}
	}
	for i := range hydrated {
		d := &hydrated[i]
		if d.Ref.Type != Broadcast {
			continue
		}
		v, ok := rows[d.Ref.ID]
		if !ok {
			continue
		}
		d.Preview = v.Summary
		if hash, ok := hashes[d.Ref.ID]; ok {
			// Do not combine current text with an older feature/content fingerprint.
			if hash != d.Version {
				d.Active = false
				continue
			}
			d.Text = v.Content + "\n" + v.Summary
		}
	}
	return nil
}
