package main

import (
	"bytes"
	"context"
	"eigenflux_server/pkg/es"
	"eigenflux_server/rpc/sort/discovery"
	"encoding/json"
	"fmt"
	"sort"
)

// Keep production BM25 scores as the anchor. The isolated index supplies only
// within-document content-change ratios, never an absolute replacement score.
func (p *poolSource) calibratedLexical(ctx context.Context, c discovery.Context, k discovery.Kind, limit int) ([]discovery.Document, error) {
	original, err := newFrozenSource(p.snap).Recall(ctx, c, k, "lexical", limit)
	if err != nil {
		return nil, err
	}
	if len(p.changed) == 0 {
		return original, nil
	}
	before, err := lexicalScores(ctx, c, p.reference)
	if err != nil {
		return nil, err
	}
	after, err := lexicalScores(ctx, c, p.lexical.BroadcastIndex)
	if err != nil {
		return nil, err
	}
	cutoff := 0.0
	for _, d := range original {
		if cutoff == 0 || d.Lexical < cutoff {
			cutoff = d.Lexical
		}
	}
	ratios := []float64{}
	prior := map[int64]discovery.Document{}
	for _, d := range original {
		prior[d.Ref.ID] = d
		if before[d.Ref.ID] > 0 && !p.stale[d.Ref.ID] {
			ratios = append(ratios, d.Lexical/before[d.Ref.ID])
		}
	}
	sort.Float64s(ratios)
	scale := 0.0
	if len(ratios) > 0 {
		scale = ratios[len(ratios)/2]
	}
	out := []discovery.Document{}
	for _, d := range original {
		id := d.Ref.ID
		if p.dropped[id] {
			continue
		}
		if p.changed[id] && !p.stale[id] && p.docs[id].Version != d.Version {
			score := after[id] * scale
			if before[id] > 0 {
				score = d.Lexical * after[id] / before[id]
			}
			if score <= 0 {
				continue
			}
			fresh := p.docs[id]
			d.Version = fresh.Version
			d.Text = fresh.Text
			d.Preview = fresh.Preview
			d.Lexical = score
		}
		out = append(out, d)
	}
	// A source already in another recall channel can newly match lexical terms.
	// Unchanged uncaptured lexical rows are not promoted by finite-corpus bias.
	for id := range p.changed {
		if _, ok := prior[id]; ok || p.dropped[id] || p.stale[id] {
			continue
		}
		if after[id] <= 0 {
			continue
		}
		// If it matched locally before but did not fill the production channel,
		// do not assume the missing index/visibility evidence was repaired.
		// With a full channel, a promotion must cross its observed cutoff.
		if before[id] > 0 && (len(original) < limit || after[id] <= before[id]) {
			continue
		}
		if scale == 0 {
			return nil, fmt.Errorf("new lexical match has no production score calibration")
		}
		d := p.docs[id]
		d.Lexical = after[id] * scale
		if before[id] > 0 && cutoff > 0 {
			base := before[id] * scale
			if base > cutoff {
				base = cutoff
			}
			d.Lexical = base * after[id] / before[id]
			if d.Lexical <= cutoff {
				continue
			}
		}
		d.DenseScore = nil
		out = append(out, d)
	}
	order := map[int64]int{}
	for i, d := range original {
		order[d.Ref.ID] = i
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Lexical != out[j].Lexical {
			return out[i].Lexical > out[j].Lexical
		}
		a, ao := order[out[i].Ref.ID]
		b, bo := order[out[j].Ref.ID]
		if ao && bo {
			return a < b
		}
		if ao != bo {
			return ao
		}
		return out[i].Ref.ID < out[j].Ref.ID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func lexicalScores(ctx context.Context, c discovery.Context, index string) (map[int64]float64, error) {
	q, err := discovery.Query(c, discovery.Broadcast, "lexical", 200)
	if err != nil {
		return nil, err
	}
	q["size"] = 10000
	q["_source"] = []string{"id"}
	b, err := json.Marshal(q)
	if err != nil {
		return nil, err
	}
	res, err := es.Client.Search(es.Client.Search.WithContext(ctx), es.Client.Search.WithIndex(index), es.Client.Search.WithBody(bytes.NewReader(b)))
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.IsError() {
		return nil, fmt.Errorf("local score lookup HTTP %d", res.StatusCode)
	}
	var data struct {
		Hits struct {
			Hits []struct {
				Score  float64            `json:"_score"`
				Source struct{ ID int64 } `json:"_source"`
			}
		}
	}
	if err = json.NewDecoder(res.Body).Decode(&data); err != nil {
		return nil, err
	}
	out := map[int64]float64{}
	for _, h := range data.Hits.Hits {
		out[h.Source.ID] = h.Score
	}
	return out, nil
}
