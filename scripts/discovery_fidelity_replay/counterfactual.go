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
	"sort"
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
	snap      *snapshot
	docs      map[int64]discovery.Document
	lexical   *discovery.Source
	dropped   map[int64]bool
	vectors   map[int64][]float32
	stale     map[int64]bool
	changed   map[int64]bool
	reference string
}

func (p *poolSource) Owner(context.Context, int64) (discovery.OwnerContext, error) {
	return discovery.OwnerContext{}, fmt.Errorf("owner outside frozen replay")
}
func (p *poolSource) Recall(ctx context.Context, c discovery.Context, k discovery.Kind, ch string, limit int) ([]discovery.Document, error) {
	if ch == "lexical" {
		return p.calibratedLexical(ctx, c, k, limit)
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
		if current, ok := p.docs[d.Ref.ID]; ok && d.Version != "" && !p.stale[d.Ref.ID] && p.changed[d.Ref.ID] {
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
			must(enc.Encode(map[string]any{"index": map[string]any{"_index": index, "_id": fmt.Sprint(d["replay_arm"]) + ":" + fmt.Sprint(d["id"])}}))
			must(enc.Encode(d))
		}
		req, err := http.NewRequest("POST", e.base+"/_bulk", &b)
		must(err)
		req.Header.Set("Content-Type", "application/x-ndjson")
		r, err := e.client.Do(req)
		must(err)
		var result struct {
			Errors bool
			Items  []map[string]struct {
				ID     string `json:"_id"`
				Status int
				Error  struct {
					Type   string
					Reason string
				}
			}
		}
		err = json.NewDecoder(r.Body).Decode(&result)
		r.Body.Close()
		must(err)
		if r.StatusCode >= 300 || result.Errors {
			for _, item := range result.Items {
				for _, v := range item {
					if v.Status >= 300 {
						panic(fmt.Sprintf("local bulk item %s: %s %.300s", v.ID, v.Error.Type, v.Error.Reason))
					}
				}
			}
			panic(fmt.Sprintf("local bulk indexing HTTP %d", r.StatusCode))
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
	baselineOnly, sourceContextOnly, variant := replayVariant()
	if os.Getenv("FIDELITY_PGC_COUPLED") == "true" {
		bodyArms = loadBodyArms(dir)
	}
	armDocs := [2]map[int64]discovery.Document{{}, {}}
	armChanged := [2]map[int64]bool{{}, {}}
	armDrops := [2]map[int64]bool{{}, {}}
	armVectors := [2]map[int64][]float32{{}, {}}
	inventory := map[int64]discovery.Document{}
	classes := map[int64]string{}
	for _, s := range snapshots {
		for id, lane := range s.Classes {
			if old, ok := classes[id]; ok && old != lane {
				panic("source class changed during capture")
			}
			classes[id] = lane
		}
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
	props, ok := tmpl.Mappings["properties"].(map[string]any)
	if !ok {
		panic("missing index properties")
	}
	props["replay_arm"] = map[string]any{"type": "keyword"}
	analysis := tmpl.Settings["analysis"]
	if index, ok := tmpl.Settings["index"].(map[string]any); ok {
		analysis = index["analysis"]
	}
	settings := map[string]any{"number_of_shards": 1, "number_of_replicas": 0, "analysis": analysis}
	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)
	names := []string{"fidelity-before-" + suffix, "fidelity-after-" + suffix}
	armPayloads := [2][]map[string]any{}
	originalPayloads := []map[string]any{}
	for i, index := range names {
		local.request("PUT", "/"+index, map[string]any{"settings": settings, "mappings": tmpl.Mappings})
		docs := []map[string]any{}
		orderedIDs := make([]int64, 0, len(inventory))
		for id := range inventory {
			orderedIDs = append(orderedIDs, id)
		}
		sort.Slice(orderedIDs, func(i, j int) bool { return orderedIDs[i] < orderedIDs[j] })
		for _, id := range orderedIDs {
			d := inventory[id]
			item, ok := byID[id]
			if !ok {
				panic(fmt.Sprintf("missing source evidence %d", id))
			}
			if d.Version != featureindex.BroadcastContentHash(id, d.AuthorID, item.Content+"\n"+item.Summary, d.Slots) {
				if item.PotentiallyEligible {
					panic(fmt.Sprintf("eligible source evidence version drift %d", id))
				}
				if i == 0 {
					fmt.Printf("inactive/seen/hard-filtered source %d has a stale authority fingerprint; retained in both pools\n", id)
				}
			}
			if i == 0 {
				doc := replayIndexDocument(id, d, item, item.Content, item.Summary)
				doc["replay_arm"] = "reference"
				originalPayloads = append(originalPayloads, doc)
			}
			summary := item.Summary
			if i == 1 && !baselineOnly && !sourceContextOnly && classes[id] == "pgc" {
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
				if classes[id] != "pgc" {
					panic("coupled source is not PGC")
				}
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
					if sourceContextOnly {
						applyReextractedExpiry(&d, &item, arm.Output.ExpireTime)
					}
					words, e := json.Marshal(arm.Output.Keywords)
					must(e)
					item.Keywords = string(words)
					armVectors[i][id] = arm.Vector
					armChanged[i][id] = true
					if arm.Output.Quality < cfg.QualityThreshold {
						armDrops[i][id] = true
						continue
					}
				}
			}
			if content != item.Content || summary != item.Summary {
				armChanged[i][id] = true
			}
			doc := replayIndexDocument(id, d, item, content, summary)
			doc["replay_arm"] = strconv.Itoa(i)
			docs = append(docs, doc)
			{
				d.Preview = summary
				d.Text = content + "\n" + summary
				d.Version = featureindex.BroadcastContentHash(id, d.AuthorID, d.Text, d.Slots)
				armDocs[i][id] = d
			}
		}
		local.bulk(index, docs)
		armPayloads[i] = docs
	}
	for i, index := range names {
		local.bulk(index, armPayloads[1-i])
		local.bulk(index, originalPayloads)
	}
	local.request("POST", "/"+strings.Join(names, ",")+"/_flush", nil)
	es.Client, err = elasticsearch.NewClient(elasticsearch.Config{Addresses: []string{base}})
	must(err)
	reports := []map[string]any{}
	for _, s := range snapshots {
		ids := []int64{}
		for _, d := range restoreDocs(s.Inventory) {
			ids = append(ids, d.Ref.ID)
		}
		stale := map[int64]bool{}
		original := map[int64]string{}
		for _, d := range s.Inventory {
			original[d.Ref.ID] = d.Version
		}
		for _, ch := range s.Channels {
			for _, d := range ch.Documents {
				if d.Version != "" && d.Version != original[d.Ref.ID] {
					stale[d.Ref.ID] = true
				}
			}
		}
		runs := []discovery.Execution{}
		for i, index := range names {
			alias := index + "-agent-" + strconv.FormatInt(s.Owner, 10)
			referenceAlias := index + "-reference-agent-" + strconv.FormatInt(s.Owner, 10)
			actions := []any{}
			for _, pair := range [][2]string{{alias, strconv.Itoa(i)}, {referenceAlias, "reference"}} {
				filters := []any{map[string]any{"terms": map[string]any{"id": ids}}, map[string]any{"term": map[string]any{"replay_arm": pair[1]}}}
				actions = append(actions, map[string]any{"add": map[string]any{"index": index, "alias": pair[0], "filter": map[string]any{"bool": map[string]any{"filter": filters}}}})
			}
			local.request("POST", "/_aliases", map[string]any{"actions": actions})
			docs := map[int64]discovery.Document{}
			vectors := map[int64][]float32{}
			changed := map[int64]bool{}
			for _, d := range restoreDocs(s.Inventory) {
				if next, ok := armDocs[i][d.Ref.ID]; ok {
					if armChanged[i][d.Ref.ID] {
						changed[d.Ref.ID] = true
						d.Preview = next.Preview
						d.Text = next.Text
						d.Version = next.Version
						d.GroupID = next.GroupID
						d.Quality = next.Quality
						d.ContentType = next.ContentType
						d.SourceType = next.SourceType
						d.Slots = next.Slots
						d.ExpiresAt = next.ExpiresAt
					}
					docs[d.Ref.ID] = d
					if v, ok := armVectors[i][d.Ref.ID]; ok {
						vectors[d.Ref.ID] = v
					}
				}
			}
			source := &poolSource{snap: s, docs: docs, dropped: armDrops[i], vectors: vectors, stale: stale, changed: changed, reference: referenceAlias, lexical: &discovery.Source{BroadcastIndex: alias}}

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
	lifecycle := "lifecycle fixed"
	if sourceContextOnly {
		lifecycle = "item IDs/creation times fixed; changed PGC expiry re-extracted in both arms"
	}
	save(filepath.Join(dir, "counterfactual-"+variant+"-manifest.json"), map[string]any{"variant": variant, "indices": names, "agents": reports, "method": "full captured candidate-union counterfactual; changed PGC extraction and embeddings rerun, other dense scores frozen; production rules/model/policies; production-score-anchored lexical deltas from finite-corpus ratios; " + lifecycle + "; changed PGC groups rerun against preceding production items"})
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

// Match the existing indexing formats, while authority keeps its own stricter
// invalid-expiry rejection. Raw SQL date strings are not ES date encodings.
func indexExpiry(raw string) string {
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
		if t, e := time.Parse(layout, raw); e == nil {
			return t.Format(time.RFC3339)
		}
	}
	return ""
}

// Source dates can change the extractor's expiry decision. Keep lexical recall
// and authoritative hydration aligned rather than freezing away that effect.
func applyReextractedExpiry(d *discovery.Document, item *itemEvidence, raw string) {
	item.ExpireTime = raw
	d.ExpiresAt = 0
	if normalized := indexExpiry(raw); normalized != "" {
		t, err := time.Parse(time.RFC3339, normalized)
		must(err)
		d.ExpiresAt = t.UnixMilli()
	}
}

func replayIndexDocument(id int64, d discovery.Document, item itemEvidence, content, summary string) map[string]any {
	keywords := []string{}
	if err := json.Unmarshal([]byte(item.Keywords), &keywords); err != nil {
		for _, word := range strings.Split(item.Keywords, ",") {
			if word = strings.TrimSpace(word); word != "" {
				keywords = append(keywords, word)
			}
		}
	}
	doc := map[string]any{"id": id, "author_agent_id": d.AuthorID, "content": content, "summary": summary, "keywords": keywords, "lang": item.Lang, "retrieval_slots": d.Slots}
	if expiry := indexExpiry(item.ExpireTime); expiry != "" {
		doc["expire_time"] = expiry
	}
	return doc
}

// Source metadata sensitivity starts from the already deployed auditor. It must
// not also apply the separate summary-fidelity rollout to unchanged documents.
func replayVariant() (baselineOnly, sourceContextOnly bool, variant string) {
	baselineOnly = os.Getenv("FIDELITY_BASELINE_ONLY") == "true"
	sourceContextOnly = os.Getenv("FIDELITY_SOURCE_CONTEXT_ONLY") == "true"
	coupled := os.Getenv("FIDELITY_PGC_COUPLED") == "true"
	if sourceContextOnly && !coupled {
		panic("source context replay requires coupled body arms")
	}
	if baselineOnly && coupled {
		panic("baseline and coupled modes are mutually exclusive")
	}
	variant = "summary"
	if baselineOnly {
		variant = "baseline"
	}
	if coupled {
		variant = "combined"
	}
	if sourceContextOnly {
		variant = "source-context"
	}
	return
}
