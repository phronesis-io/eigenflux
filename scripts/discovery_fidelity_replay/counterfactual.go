package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/es"
	"eigenflux_server/pkg/featureindex"
	"eigenflux_server/rpc/sort/discovery"
	elasticsearch "github.com/elastic/go-elasticsearch/v8"
)

type auditOutput struct {
	ID         string `json:"id"`
	Summary    string `json:"summary"`
	Error      string `json:"error"`
	InputHash  string `json:"input_hash"`
	PromptHash string `json:"prompt_hash"`
}
type poolSource struct {
	snap    *snapshot
	docs    map[int64]discovery.Document
	lexical *discovery.Source
	dropped map[int64]bool
	vectors map[int64][]float32
}

func (p *poolSource) Owner(context.Context, int64) (discovery.OwnerContext, error) {
	return discovery.OwnerContext{}, fmt.Errorf("owner outside frozen replay")
}
func (p *poolSource) Recall(ctx context.Context, c discovery.Context, k discovery.Kind, ch string, limit int) ([]discovery.Document, error) {
	if ch == "lexical" {
		return p.lexical.Recall(ctx, c, k, ch, limit)
	}
	docs, err := newFrozenSource(p.snap).Recall(ctx, c, k, ch, limit)
	if err != nil {
		return nil, err
	}
	if ch == "dense" && len(p.vectors) > 0 {
		return p.dense(c, docs, limit)
	}
	out := []discovery.Document{}
	for _, d := range docs {
		if p.dropped[d.Ref.ID] {
			continue
		}
		if current, ok := p.docs[d.Ref.ID]; ok && d.Version != "" {
			d.Version = current.Version
			d.Text = current.Text
			d.Preview = current.Preview
		}
		out = append(out, d)
	}
	return out, nil
}
func (p *poolSource) Hydrate(_ context.Context, _ int64, _ discovery.Mode, docs []discovery.Document) ([]discovery.Document, error) {
	out := []discovery.Document{}
	for _, d := range docs {
		if p.dropped[d.Ref.ID] {
			continue
		}
		if fresh, ok := p.docs[d.Ref.ID]; ok {
			out = append(out, fresh)
		}
	}
	return out, nil
}
func (p *poolSource) Seen(ctx context.Context, owner int64, docs []discovery.Document) (map[string]bool, error) {
	return newFrozenSource(p.snap).Seen(ctx, owner, docs)
}

type localES struct {
	base   string
	client *http.Client
}

func (e localES) request(method, path string, body any) map[string]any {
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		must(err)
	}
	req, err := http.NewRequest(method, e.base+path, bytes.NewReader(data))
	must(err)
	req.Header.Set("Content-Type", "application/json")
	r, err := e.client.Do(req)
	must(err)
	defer r.Body.Close()
	b, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	must(err)
	if r.StatusCode >= 300 {
		panic(fmt.Sprintf("local ES %s %s: %d %s", method, path, r.StatusCode, string(b)))
	}
	var out map[string]any
	must(json.Unmarshal(b, &out))
	return out
}
func (e localES) bulk(index string, docs []map[string]any) {
	for i := 0; i < len(docs); i += 100 {
		end := i + 100
		if end > len(docs) {
			end = len(docs)
		}
		var b bytes.Buffer
		enc := json.NewEncoder(&b)
		for _, d := range docs[i:end] {
			must(enc.Encode(map[string]any{"index": map[string]any{"_index": index, "_id": fmt.Sprint(d["id"])}}))
			must(enc.Encode(d))
		}
		req, err := http.NewRequest("POST", e.base+"/_bulk", &b)
		must(err)
		req.Header.Set("Content-Type", "application/x-ndjson")
		r, err := e.client.Do(req)
		must(err)
		var result struct{ Errors bool }
		err = json.NewDecoder(r.Body).Decode(&result)
		r.Body.Close()
		must(err)
		if r.StatusCode >= 300 || result.Errors {
			panic("local bulk indexing failed")
		}
	}
	e.request("POST", "/"+index+"/_refresh", nil)
}
func loadSnapshots(dir string) []*snapshot {
	files, err := filepath.Glob(filepath.Join(dir, "agent-*.json"))
	must(err)
	var out []*snapshot
	for _, f := range files {
		b, err := os.ReadFile(f)
		must(err)
		var s snapshot
		must(json.Unmarshal(b, &s))
		if s.CaptureError != "" || len(s.Inventory) == 0 {
			panic(fmt.Sprintf("incomplete Agent %d", s.Owner))
		}
		for i := range s.Contexts {
			s.Contexts[i].Vector = s.Vectors[s.Contexts[i].ID]
		}
		out = append(out, &s)
	}
	cohortBytes, err := os.ReadFile(filepath.Join(dir, "cohort.json"))
	must(err)
	var cohort []struct{ AgentID int64 }
	must(json.Unmarshal(cohortBytes, &cohort))
	actual := map[int64]bool{}
	for _, s := range out {
		actual[s.Owner] = true
	}
	if len(out) != len(cohort) || len(out) == 0 {
		panic("incomplete fixed cohort")
	}
	for _, a := range cohort {
		if !actual[a.AgentID] {
			panic("fixed cohort Agent missing")
		}
	}
	return out
}
func counterfactual(cfg *config.Config, dir string) {
	base := os.Getenv("FIDELITY_LOCAL_ES_URL")
	u, err := url.Parse(base)
	must(err)
	if u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() != "56921" {
		panic("counterfactual ES must be isolated loopback port 56921")
	}
	local := localES{base, &http.Client{Timeout: 60 * time.Second}}
	var items []itemEvidence
	b, err := os.ReadFile(filepath.Join(dir, "items.json"))
	must(err)
	must(json.Unmarshal(b, &items))
	byID := map[int64]itemEvidence{}
	for _, it := range items {
		byID[it.ItemID] = it
	}
	audits := map[int64]auditOutput{}
	f, err := os.Open(filepath.Join(dir, "audits.jsonl"))
	must(err)
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 2<<20)
	for scanner.Scan() {
		var a auditOutput
		must(json.Unmarshal(scanner.Bytes(), &a))
		id, err := strconv.ParseInt(a.ID, 10, 64)
		must(err)
		audits[id] = a
	}
	must(scanner.Err())
	prompt, err := os.ReadFile("static/templates/prompts/summary_grounding.tmpl")
	must(err)
	promptHash := fmt.Sprintf("%x", sha256.Sum256(prompt))
	for _, item := range items {
		if a, ok := audits[item.ItemID]; ok {
			if a.PromptHash != promptHash || a.InputHash != auditInputHash(item) {
				panic(fmt.Sprintf("stale audit evidence %d", item.ItemID))
			}
		}
	}
	snapshots := loadSnapshots(dir)
	bodyArms := [2]map[int64]bodyArm{{}, {}}
	variant := "summary"
	if os.Getenv("FIDELITY_PGC_COUPLED") == "true" {
		bodyArms = loadBodyArms(dir)
		variant = "combined"
	}
	armDocs := [2]map[int64]discovery.Document{{}, {}}
	armDrops := [2]map[int64]bool{{}, {}}
	armVectors := [2]map[int64][]float32{{}, {}}
	inventory := map[int64]discovery.Document{}
	for _, s := range snapshots {
		for _, d := range restoreDocs(s.Inventory) {
			if old, ok := inventory[d.Ref.ID]; ok && old.Version != d.Version {
				panic("candidate changed during capture; recapture cohort")
			}
			inventory[d.Ref.ID] = d
		}
	}
	// Same analyzers/mappings as production. Corpus statistics here are those of
	// the full captured union, not the two-million-document production corpus.
	var template struct {
		IndexTemplates []struct {
			IndexTemplate struct {
				Template struct {
					Settings map[string]any
					Mappings map[string]any
				}
			} `json:"index_template"`
		} `json:"index_templates"`
	}
	b, err = os.ReadFile(filepath.Join(dir, "index-template.json"))
	must(err)
	must(json.Unmarshal(b, &template))
	if len(template.IndexTemplates) != 1 {
		panic("missing frozen index template")
	}
	tmpl := template.IndexTemplates[0].IndexTemplate.Template
	analysis := tmpl.Settings["analysis"]
	if index, ok := tmpl.Settings["index"].(map[string]any); ok {
		analysis = index["analysis"]
	}
	settings := map[string]any{"number_of_shards": 1, "number_of_replicas": 0, "analysis": analysis}
	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	names := []string{"fidelity-before-" + suffix, "fidelity-after-" + suffix}
	for i, index := range names {
		local.request("PUT", "/"+index, map[string]any{"settings": settings, "mappings": tmpl.Mappings})
		docs := []map[string]any{}
		for id, d := range inventory {
			item, ok := byID[id]
			if !ok {
				panic(fmt.Sprintf("missing source evidence %d", id))
			}
			if d.Version != featureindex.BroadcastContentHash(id, d.AuthorID, item.Content+"\n"+item.Summary, d.Slots) {
				panic(fmt.Sprintf("source evidence version drift %d", id))
			}
			summary := item.Summary
			if i == 1 {
				a, ok := audits[id]
				if !ok && item.PotentiallyEligible {
					panic(fmt.Sprintf("missing candidate audit %d", id))
				}
				if !ok {
					a.Summary = item.Summary
				}
				if a.Error != "" {
					armDrops[i][id] = true
					continue
				}
				summary = a.Summary
			}
			content := item.Content
			if arm, ok := bodyArms[i][id]; ok {
				if arm.Drop {
					armDrops[i][id] = true
					continue
				}
				if arm.Content != item.Content && arm.Output == nil {
					panic(fmt.Sprintf("missing coupled extraction %d arm %d", id, i))
				}
				if arm.Output != nil {
					content = arm.Content
					summary = arm.Output.Summary
					if arm.GroupID <= 0 {
						panic(fmt.Sprintf("missing dedup replay %d arm %d", id, i))
					}
					d.GroupID = arm.GroupID
					d.Quality = arm.Output.Quality
					d.ContentType = arm.Output.BroadcastType
					d.SourceType = arm.Output.SourceType
					d.Slots.Lang = []string{arm.Output.Lang}
					item.Lang = arm.Output.Lang
					words, e := json.Marshal(arm.Output.Keywords)
					must(e)
					item.Keywords = string(words)
					armVectors[i][id] = arm.Vector
					if arm.Output.Quality < cfg.QualityThreshold {
						armDrops[i][id] = true
						continue
					}
				}
			}
			keywords := []string{}
			if err := json.Unmarshal([]byte(item.Keywords), &keywords); err != nil {
				for _, word := range strings.Split(item.Keywords, ",") {
					if word = strings.TrimSpace(word); word != "" {
						keywords = append(keywords, word)
					}
				}
			}
			doc := map[string]any{"id": id, "author_agent_id": d.AuthorID, "content": content, "summary": summary, "keywords": keywords, "lang": item.Lang, "retrieval_slots": d.Slots}
			if item.ExpireTime != "" {
				doc["expire_time"] = item.ExpireTime
			}
			docs = append(docs, doc)
			{
				d.Preview = summary
				d.Text = content + "\n" + summary
				d.Version = featureindex.BroadcastContentHash(id, d.AuthorID, d.Text, d.Slots)
				armDocs[i][id] = d
			}
		}
		local.bulk(index, docs)
	}
	es.Client, err = elasticsearch.NewClient(elasticsearch.Config{Addresses: []string{base}})
	must(err)
	reports := []map[string]any{}
	for _, s := range snapshots {
		ids := []int64{}
		for _, d := range restoreDocs(s.Inventory) {
			ids = append(ids, d.Ref.ID)
		}
		runs := []discovery.Execution{}
		for i, index := range names {
			alias := index + "-agent-" + strconv.FormatInt(s.Owner, 10)
			local.request("POST", "/_aliases", map[string]any{"actions": []any{map[string]any{"add": map[string]any{"index": index, "alias": alias, "filter": map[string]any{"terms": map[string]any{"id": ids}}}}}})
			docs := map[int64]discovery.Document{}
			vectors := map[int64][]float32{}
			for _, d := range restoreDocs(s.Inventory) {
				if next, ok := armDocs[i][d.Ref.ID]; ok {
					d.Preview = next.Preview
					d.Text = next.Text
					d.Version = next.Version
					d.GroupID = next.GroupID
					d.Quality = next.Quality
					d.ContentType = next.ContentType
					d.SourceType = next.SourceType
					d.Slots = next.Slots
					docs[d.Ref.ID] = d
					if v, ok := armVectors[i][d.Ref.ID]; ok {
						vectors[d.Ref.ID] = v
					}
				}
			}
			source := &poolSource{snap: s, docs: docs, dropped: armDrops[i], vectors: vectors, lexical: &discovery.Source{BroadcastIndex: alias}}

			engine, close := engine(cfg, dir, s, source)
			run, err := engine.ReplayContexts(context.Background(), s.Owner, s.Contexts, []discovery.Kind{discovery.Broadcast}, 20, s.At)
			close()
			must(err)
			if len(run.PartialReasons) > 0 {
				for _, r := range run.PartialReasons {
					if strings.Contains(r, "unavailable") {
						panic("partial counterfactual execution")
					}
				}
			}
			runs = append(runs, run)
		}
		save(filepath.Join(dir, fmt.Sprintf("comparison-%s-%d.json", variant, s.Owner)), map[string]any{"before": runs[0], "after": runs[1]})
		reports = append(reports, map[string]any{"agent_id": strconv.FormatInt(s.Owner, 10), "pool": len(ids), "captured_delivered": len(s.Before.Candidates), "before_delivered": len(runs[0].Candidates), "after_delivered": len(runs[1].Candidates)})
		fmt.Printf("Agent %d: full pool %d; captured %d; finite-corpus before/after %d/%d\n", s.Owner, len(ids), len(s.Before.Candidates), len(runs[0].Candidates), len(runs[1].Candidates))
	}
	save(filepath.Join(dir, "counterfactual-"+variant+"-manifest.json"), map[string]any{"variant": variant, "indices": names, "agents": reports, "method": "full captured candidate-union counterfactual; changed PGC extraction and embeddings rerun, other dense scores frozen; production rules/model/policies; finite-corpus BM25 statistics; lifecycle fixed; changed PGC groups rerun against preceding production items"})
}

func auditInputHash(item itemEvidence) string {
	parts := []string{}
	for _, value := range []string{item.Content, item.Notes, item.Summary} {
		var b bytes.Buffer
		e := json.NewEncoder(&b)
		e.SetEscapeHTML(false)
		must(e.Encode(value))
		encoded := strings.TrimSuffix(b.String(), "\n")
		var normalized strings.Builder
		for n := 0; n < len(encoded); n++ {
			if encoded[n] == '\\' && n+1 < len(encoded) {
				if strings.HasPrefix(encoded[n:], `\u2028`) {
					normalized.WriteRune('\u2028')
					n += 5
					continue
				}
				if strings.HasPrefix(encoded[n:], `\u2029`) {
					normalized.WriteRune('\u2029')
					n += 5
					continue
				}
				normalized.WriteByte(encoded[n])
				n++
				normalized.WriteByte(encoded[n])
				continue
			}
			normalized.WriteByte(encoded[n])
		}
		encoded = normalized.String()
		parts = append(parts, encoded)
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte("["+strings.Join(parts, ", ")+"]")))
}
