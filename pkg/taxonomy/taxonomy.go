// Package taxonomy owns immutable canonical vocabulary shared by input and
// content projections. Missing mappings remain unknown.
package taxonomy

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
)

type Node struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Category string    `json:"category,omitempty"`
	Subtype  string    `json:"subtype,omitempty"`
	Aliases  []string  `json:"aliases,omitempty"`
	Vector   []float32 `json:"embedding,omitempty"`
}
type Vocabulary struct {
	Version          string `json:"version"`
	EmbeddingVersion string `json:"embedding_version"`
	Categories       []Node `json:"categories"`
	Subtypes         []Node `json:"subtypes"`
	Intents          []Node `json:"intents"`
}
type Match struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Category   string  `json:"category"`
	Subtype    string  `json:"subtype,omitempty"`
	Similarity float64 `json:"similarity"`
	Version    string  `json:"taxonomy_version"`
}

func Load(r io.Reader) (*Vocabulary, error) {
	var v Vocabulary
	d := json.NewDecoder(r)
	d.DisallowUnknownFields()
	if err := d.Decode(&v); err != nil {
		return nil, err
	}
	var trailing any
	if err := d.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("trailing taxonomy data")
	}
	if err := v.Validate(); err != nil {
		return nil, err
	}
	return &v, nil
}
func (v *Vocabulary) Validate() error {
	if v.Version == "" || len(v.Categories) == 0 {
		return fmt.Errorf("empty taxonomy")
	}
	ids := map[string]bool{}
	for _, nodes := range [][]Node{v.Categories, v.Subtypes, v.Intents} {
		for _, n := range nodes {
			if n.ID == "" || n.Name == "" || ids[n.ID] {
				return fmt.Errorf("invalid or duplicate taxonomy node")
			}
			ids[n.ID] = true
			for _, x := range n.Vector {
				if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
					return fmt.Errorf("nonfinite taxonomy embedding")
				}
			}
		}
	}
	for _, n := range v.Subtypes {
		if !v.Category(n.Category) {
			return fmt.Errorf("unknown category")
		}
	}
	for _, n := range v.Intents {
		if !v.ValidBranch(n.Category, n.Subtype) {
			return fmt.Errorf("unknown intent parent")
		}
	}
	return nil
}
func (v *Vocabulary) Category(id string) bool {
	for _, n := range v.Categories {
		if n.ID == id {
			return true
		}
	}
	return false
}
func (v *Vocabulary) ValidBranch(category, subtype string) bool {
	if !v.Category(category) {
		return false
	}
	if subtype == "" {
		return true
	}
	for _, n := range v.Subtypes {
		if n.ID == subtype && n.Category == category {
			return true
		}
	}
	return false
}
func (v *Vocabulary) Intent(id, category, subtype string) bool {
	for _, n := range v.Intents {
		if n.ID == id && (category == "" || n.Category == category) && (subtype == "" || n.Subtype == subtype) {
			return true
		}
	}
	return false
}
func (v *Vocabulary) Search(phrase, category, subtype string, vector []float32, threshold float64, limit int) []Match {
	out := []Match{}
	phrase = strings.ToLower(strings.TrimSpace(phrase))
	if phrase == "" || limit < 1 {
		return out
	}
	if limit > 20 {
		limit = 20
	}
	for _, n := range v.Intents {
		if category != "" && n.Category != category || subtype != "" && n.Subtype != subtype {
			continue
		}
		score := 0.0
		for _, label := range append([]string{n.Name, n.ID}, n.Aliases...) {
			if strings.ToLower(strings.TrimSpace(label)) == phrase {
				score = 1
				break
			}
		}
		if score == 0 && len(vector) > 0 && len(vector) == len(n.Vector) {
			var dot, a, b float64
			for i, x := range vector {
				dot += float64(x) * float64(n.Vector[i])
				a += float64(x) * float64(x)
				b += float64(n.Vector[i]) * float64(n.Vector[i])
			}
			if a > 0 && b > 0 {
				score = dot / math.Sqrt(a*b)
			}
		}
		if score >= threshold && score > 0 {
			out = append(out, Match{n.ID, n.Name, n.Category, n.Subtype, score, v.Version})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Similarity != out[j].Similarity {
			return out[i].Similarity > out[j].Similarity
		}
		return out[i].ID < out[j].ID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// ValidateEmbedding prevents incompatible vectors from silently becoming zero
// similarity. Missing vectors are permitted for canonical-only vocabulary rows.
func (v *Vocabulary) ValidateEmbedding(version string, dimensions int) error {
	if dimensions <= 0 || version == "" || v.EmbeddingVersion != version {
		return fmt.Errorf("taxonomy embedding version mismatch")
	}
	for _, nodes := range [][]Node{v.Categories, v.Subtypes, v.Intents} {
		for _, n := range nodes {
			if len(n.Vector) != 0 && len(n.Vector) != dimensions {
				return fmt.Errorf("taxonomy embedding dimension mismatch: %s", n.ID)
			}
		}
	}
	return nil
}
