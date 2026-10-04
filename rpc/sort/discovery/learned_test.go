package discovery

import (
	"eigenflux_server/rpc/sort/discoverylr"
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

type learnedFake struct{ calls int }

func (f *learnedFake) ScoreBatch(v [][]float64) ([]discoverylr.Result, bool) {
	f.calls++
	out := make([]discoverylr.Result, len(v))
	for i, x := range v {
		out[i] = discoverylr.Result{ModelVersion: "test", Contract: discoverylr.Contract, Probability: x[2]}
	}
	return out, true
}

func TestLearningVectorMatchesPythonFixture(t *testing.T) {
	raw, err := os.ReadFile("../discoverylr/testdata/discovery_lr_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		Candidate Candidate `json:"candidate"`
		Features  []float64 `json:"features"`
	}
	if err = json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	data.Candidate.LR = &discoverylr.Result{Probability: .99}
	data.Candidate.FinalScore = .99
	got, ok := learningVector(data.Candidate)
	if !ok || !reflect.DeepEqual(got, data.Features) {
		t.Fatalf("features=%v want=%v", got, data.Features)
	}
}

func TestLearnedRankingPreservesEligibilityModeKindAndContextOrder(t *testing.T) {
	raw, _ := os.ReadFile("../discoverylr/testdata/discovery_lr_v1.json")
	var data struct {
		Candidate Candidate `json:"candidate"`
	}
	_ = json.Unmarshal(raw, &data)
	first := data.Candidate
	first.Score.Eligible = true
	first.Score.Value = .9
	first.Document.Ref.ID = 1
	first.Score.Features = map[string]float64{"lexical": .2, "semantic": 0, "quality": .5, "freshness": .1}
	second := first
	second.Document.Ref.ID = 2
	second.Score.Value = .3
	second.Score.Features = map[string]float64{"lexical": .2, "semantic": 0, "quality": .5, "freshness": .8}
	agent := first
	agent.Document.Ref = SourceRef{Type: Agent, ID: 3}
	rejected := first
	rejected.Document.Ref.ID = 4
	rejected.Score.Eligible = false
	fake := &learnedFake{}
	engine := &Engine{Learned: fake}
	search := []Candidate{first, second, agent}
	engine.rankLearned(search, Search)
	if fake.calls != 0 {
		t.Fatal("search invoked learned model")
	}
	in := []Candidate{first, second, agent, rejected}
	engine.rankLearned(in, Recommendation)
	if in[2].LR != nil || in[3].LR != nil || in[3].Score.Eligible {
		t.Fatal("changed kind or eligibility")
	}
	if in[0].Score.Value != .9 {
		t.Fatal("lost frozen rule baseline")
	}
	out := Merge(in, []Kind{Broadcast, Agent}, Recommendation, 10)
	if out[0].Document.Ref.ID != 2 || len(out) != 3 {
		t.Fatalf("unexpected merge: %+v", out)
	}
	// A lower-priority context still cannot leapfrog a higher-priority context.
	in[0].Context.Priority = 10
	out = Merge(in, []Kind{Broadcast, Agent}, Recommendation, 10)
	if out[0].Document.Ref.ID != 1 {
		t.Fatal("LR overrode context priority")
	}
	// Policy-reserved order remains stronger than probability.
	in[0].Context.Priority = 0
	in[0].Order = 1
	in[1].Order = 2
	out = Merge(in, []Kind{Broadcast, Agent}, Recommendation, 10)
	if out[0].Document.Ref.ID != 1 {
		t.Fatal("LR overrode policy order")
	}
}
