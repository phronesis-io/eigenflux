package main

import (
	"bufio"
	"eigenflux_server/pipeline/llm"
	"eigenflux_server/rpc/sort/discovery"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
)

type bodyArm struct {
	GroupID int64
	Content string
	Vector  []float32
	Output  *llm.ExtractResult
	Drop    bool
}

func loadBodyArms(dir string) [2]map[int64]bodyArm {
	arms := [2]map[int64]bodyArm{{}, {}}
	read := func(name string, handle func([]byte)) {
		f, e := os.Open(filepath.Join(dir, name))
		must(e)
		defer f.Close()
		s := bufio.NewScanner(f)
		s.Buffer(make([]byte, 4096), 4<<20)
		for s.Scan() {
			handle(s.Bytes())
		}
		must(s.Err())
	}
	read("pgc-attribution-paired.jsonl", func(b []byte) {
		var r struct {
			ID      string
			Enabled bool
			Error   string
			Output  struct {
				Discard bool
				Content string
			}
		}
		must(json.Unmarshal(b, &r))
		id, e := strconv.ParseInt(r.ID, 10, 64)
		must(e)
		i := 0
		if r.Enabled {
			i = 1
		}
		arms[i][id] = bodyArm{Content: r.Output.Content, Drop: r.Output.Discard || r.Error != ""}
	})
	read("pgc-core-paired.jsonl", func(b []byte) {
		var r struct {
			Sample struct {
				ID      string
				Content string
				Enabled bool
			}
			Output *llm.ExtractResult
			Vector []float32
			Error  string
		}
		must(json.Unmarshal(b, &r))
		id, e := strconv.ParseInt(r.Sample.ID, 10, 64)
		must(e)
		i := 0
		if r.Sample.Enabled {
			i = 1
		}
		arm, ok := arms[i][id]
		if !ok || arm.Content != r.Sample.Content {
			return
		}
		if r.Error != "" {
			arm.Drop = true
		} else {
			arm.Output = r.Output
			arm.Vector = r.Vector
			arm.Drop = r.Output == nil || r.Output.Discard
		}
		arms[i][id] = arm
	})
	read("dedup-neighbors.jsonl", func(b []byte) {
		var r struct {
			ID      string
			Enabled bool
			GroupID string `json:"group_id"`
			Error   string
		}
		must(json.Unmarshal(b, &r))
		if r.Error != "" {
			return
		}
		id, e := strconv.ParseInt(r.ID, 10, 64)
		must(e)
		group, e := strconv.ParseInt(r.GroupID, 10, 64)
		must(e)
		i := 0
		if r.Enabled {
			i = 1
		}
		a, ok := arms[i][id]
		if ok {
			a.GroupID = group
			arms[i][id] = a
		}
	})
	return arms
}
func cosineScore(a, b []float32) (float64, error) {
	if len(a) == 0 || len(a) != len(b) {
		return 0, fmt.Errorf("incompatible replay vectors")
	}
	var dot, aa, bb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		aa += x * x
		bb += y * y
	}
	if aa == 0 || bb == 0 {
		return 0, fmt.Errorf("zero replay vector")
	}
	return (1 + dot/math.Sqrt(aa*bb)) / 2, nil
}
func (p *poolSource) dense(c discovery.Context, docs []discovery.Document, limit int) ([]discovery.Document, error) {
	byID := map[int64]discovery.Document{}
	for _, d := range docs {
		if fresh, ok := p.docs[d.Ref.ID]; ok && d.Version != "" && !p.stale[d.Ref.ID] && p.changed[d.Ref.ID] {
			d.Version = fresh.Version
			d.Text = fresh.Text
			d.Preview = fresh.Preview
		}
		if !p.dropped[d.Ref.ID] {
			byID[d.Ref.ID] = d
		}
	}
	for id, v := range p.vectors {
		d, ok := p.docs[id]
		if !ok || p.dropped[id] || p.stale[id] {
			continue
		}
		if !matchesRecallFilters(c, d) {
			delete(byID, id)
			continue
		}
		score, err := cosineScore(c.Vector, v)
		if err != nil {
			return nil, err
		}
		d.DenseScore = &score
		d.Lexical = 0
		byID[id] = d
	}
	out := []discovery.Document{}
	for _, d := range byID {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].DenseScore, out[j].DenseScore
		if a == nil {
			return false
		}
		if b == nil {
			return true
		}
		if *a == *b {
			return out[i].Ref.ID < out[j].Ref.ID
		}
		return *a > *b
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Apply exactly the positive Broadcast retrieval constraints before dense top-k;
// visibility, expiry and text exclusions remain in the normal hydration filter.
func matchesRecallFilters(c discovery.Context, d discovery.Document) bool {
	c.OwnerID = -1
	c.UnverifiedNeedReason = ""
	c.Filters = discovery.Filters{Lang: c.Filters.Lang, ProviderRegion: c.Filters.ProviderRegion, ExcludeAuthors: c.Filters.ExcludeAuthors}
	d.Active = true
	d.Visible = true
	d.Blocked = false
	d.ExpiresAt = 0
	return discovery.Check(c, d, discovery.Recommendation, c.CreatedAt) == ""
}
