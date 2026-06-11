// Package agti implements the AgentRapport quiz ("你和你的 Agent 是什么关系"),
// a public marketing activity. An agent answers 10 questions about its human
// (commit-reveal locked), the human answers the same questions, and the engine
// maps the comparison to one of 10 relationship types.
//
// Question bank and type copy live in static/agti/*.json so campaign copy can
// be tuned with a file edit + restart, without a rebuild.
package agti

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
)

// Option is one answer choice. Energy/Polite/Rare are scoring metadata the
// engine consumes; they are never sent to clients.
type Option struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Energy int    `json:"energy,omitempty"`
	Polite bool   `json:"polite,omitempty"`
	Rare   bool   `json:"rare,omitempty"`
}

// Question is one bank entry. Key marks a "关键题" (a miss on it is a strong
// signal for the NPC type).
type Question struct {
	ID        string   `json:"id"`
	Dimension string   `json:"dimension,omitempty"`
	Key       bool     `json:"key,omitempty"`
	Text      string   `json:"text"`
	Options   []Option `json:"options"`
}

// TypeInfo is one relationship type's display copy.
type TypeInfo struct {
	Code    string `json:"code"`
	Name    string `json:"name"`
	Emoji   string `json:"emoji"`
	Color   string `json:"color"`
	Role    string `json:"role"`
	Tagline string `json:"tagline"`
	Desc    string `json:"desc"`
}

// Bank holds the loaded question bank and type table.
type Bank struct {
	PickCount int
	Items     []Question
	Types     map[string]TypeInfo
	byID      map[string]*Question
}

type questionsFile struct {
	PickCount int        `json:"pickCount"`
	Items     []Question `json:"items"`
}

type typesFile struct {
	Types map[string]TypeInfo `json:"types"`
}

// LoadBank reads questions.json and types.json from dir (static/agti).
func LoadBank(dir string) (*Bank, error) {
	var qf questionsFile
	if err := readJSONFile(filepath.Join(dir, "questions.json"), &qf); err != nil {
		return nil, fmt.Errorf("agti questions: %w", err)
	}
	var tf typesFile
	if err := readJSONFile(filepath.Join(dir, "types.json"), &tf); err != nil {
		return nil, fmt.Errorf("agti types: %w", err)
	}
	if len(qf.Items) == 0 || len(tf.Types) == 0 {
		return nil, fmt.Errorf("agti bank empty: %d questions, %d types", len(qf.Items), len(tf.Types))
	}
	if qf.PickCount <= 0 {
		qf.PickCount = 10
	}
	b := &Bank{PickCount: qf.PickCount, Items: qf.Items, Types: tf.Types, byID: make(map[string]*Question, len(qf.Items))}
	for i := range b.Items {
		b.byID[b.Items[i].ID] = &b.Items[i]
	}
	return b, nil
}

func readJSONFile(path string, v interface{}) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// Pick returns PickCount random questions (Fisher–Yates over a copy).
func (b *Bank) Pick() []Question {
	items := make([]Question, len(b.Items))
	copy(items, b.Items)
	rand.Shuffle(len(items), func(i, j int) { items[i], items[j] = items[j], items[i] })
	k := b.PickCount
	if k > len(items) {
		k = len(items)
	}
	return items[:k]
}

// Get returns the bank question by ID, or nil.
func (b *Bank) Get(id string) *Question {
	return b.byID[id]
}

// NormalizeAnswers mirrors the demo's normAnswers: any missing or invalid
// answer falls back to the question's first option, so the engine always sees
// a complete answer map.
func NormalizeAnswers(questions []Question, answers map[string]string) map[string]string {
	out := make(map[string]string, len(questions))
	for _, q := range questions {
		v := answers[q.ID]
		valid := false
		for _, o := range q.Options {
			if o.Key == v {
				valid = true
				break
			}
		}
		if valid {
			out[q.ID] = v
		} else {
			out[q.ID] = q.Options[0].Key
		}
	}
	return out
}
