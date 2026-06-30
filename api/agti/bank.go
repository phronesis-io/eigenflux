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

// PickRule selects Count questions from one named sub-bank (by Dimension).
// e.g. {use, "AI 使用题", 5} + {self, "自我认知题", 5} → 10 题/场.
type PickRule struct {
	Dimension string `json:"dimension"`
	Name      string `json:"name"`
	Count     int    `json:"count"`
}

// Bank holds the loaded question bank and type table.
type Bank struct {
	PickCount int
	PickSpec  []PickRule // 分类抽题规则；为空时退回从全库随机抽 PickCount 道
	Items     []Question
	Types     map[string]TypeInfo
	byID      map[string]*Question
}

type questionsFile struct {
	PickCount int        `json:"pickCount"`
	Pick      []PickRule `json:"pick"`
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
	// 分类抽题规则优先：校验每个子题库题量足够，并把 PickCount 设为各类之和。
	if len(qf.Pick) > 0 {
		sum := 0
		for _, r := range qf.Pick {
			avail := 0
			for _, q := range qf.Items {
				if q.Dimension == r.Dimension {
					avail++
				}
			}
			if avail < r.Count {
				return nil, fmt.Errorf("agti pick: 子题库 %q(%s) 需要 %d 题，只有 %d 题", r.Name, r.Dimension, r.Count, avail)
			}
			sum += r.Count
		}
		qf.PickCount = sum
	}
	if qf.PickCount <= 0 {
		qf.PickCount = 10
	}
	b := &Bank{PickCount: qf.PickCount, PickSpec: qf.Pick, Items: qf.Items, Types: tf.Types, byID: make(map[string]*Question, len(qf.Items))}
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

// Pick returns one quiz's questions. With a PickSpec it draws Count random
// questions from each named sub-bank (e.g. 5 AI 使用题 + 5 自我认知题) and
// shuffles them together; otherwise it falls back to PickCount random over the
// whole bank.
func (b *Bank) Pick() []Question {
	if len(b.PickSpec) > 0 {
		return b.pickByCategory()
	}
	items := make([]Question, len(b.Items))
	copy(items, b.Items)
	rand.Shuffle(len(items), func(i, j int) { items[i], items[j] = items[j], items[i] })
	k := b.PickCount
	if k > len(items) {
		k = len(items)
	}
	return items[:k]
}

// pickByCategory draws each PickRule.Count random questions from its dimension
// pool, then shuffles the combined set so the two sub-banks interleave.
func (b *Bank) pickByCategory() []Question {
	out := make([]Question, 0, b.PickCount)
	for _, rule := range b.PickSpec {
		pool := make([]Question, 0, len(b.Items))
		for _, q := range b.Items {
			if q.Dimension == rule.Dimension {
				pool = append(pool, q)
			}
		}
		rand.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
		k := rule.Count
		if k > len(pool) {
			k = len(pool)
		}
		out = append(out, pool[:k]...)
	}
	rand.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
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
