package agti

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"testing"
)

// TestUpdateGolden regenerates testdata/golden.json against the CURRENT bank
// (static/agti/questions.json). Run with GOLDEN_UPDATE=1 after swapping the
// question bank. It searches deterministic random answer patterns until every
// one of the 10 relationship codes is produced, then records Analyze's exact
// output as the regression baseline. The Go engine is the source of truth here
// (same logic as the frontend engine.js).
func TestUpdateGolden(t *testing.T) {
	if os.Getenv("GOLDEN_UPDATE") == "" {
		t.Skip("set GOLDEN_UPDATE=1 to regenerate testdata/golden.json")
	}
	bank := loadTestBank(t)
	items := bank.Items
	rng := rand.New(rand.NewSource(1))

	want := []string{"SOUL", "GANG", "XRAY", "NPC", "KISS", "CTRL", "RIDDLE", "VS", "MAMA", "FAKE"}
	const target = 2
	perCode := map[string][]goldenCase{}
	matchProbs := []float64{1.0, 0.95, 0.9, 0.8, 0.7, 0.6, 0.5, 0.4, 0.3, 0.2, 0.1, 0.0}

	// indices of key questions, to deliberately steer NPC (needs a key-question miss)
	var keyIdx []int
	for i, q := range items {
		if q.Key {
			keyIdx = append(keyIdx, i)
		}
	}

	done := func() bool {
		for _, c := range want {
			if len(perCode[c]) < target {
				return false
			}
		}
		return true
	}

	for iter := 0; iter < 5_000_000 && !done(); iter++ {
		perm := rng.Perm(len(items))[:bank.PickCount]
		// Every other iteration, force a key question into the subset so NPC is reachable.
		forceKeyID := ""
		if iter%2 == 0 && len(keyIdx) > 0 {
			perm[0] = keyIdx[rng.Intn(len(keyIdx))]
			forceKeyID = items[perm[0]].ID
		}
		qs := make([]Question, 0, bank.PickCount)
		for _, idx := range perm {
			qs = append(qs, items[idx])
		}
		p := matchProbs[rng.Intn(len(matchProbs))]
		agent := map[string]string{}
		human := map[string]string{}
		for _, q := range qs {
			ak := q.Options[rng.Intn(len(q.Options))].Key
			agent[q.ID] = ak
			if rng.Float64() < p {
				human[q.ID] = ak
			} else {
				human[q.ID] = q.Options[rng.Intn(len(q.Options))].Key
			}
		}
		// Bias toward an NPC-style miss on the forced key question (high match elsewhere).
		if forceKeyID != "" && p >= 0.8 {
			q := qs[0]
			for _, o := range q.Options {
				if o.Key != agent[forceKeyID] {
					human[forceKeyID] = o.Key
					break
				}
			}
		}

		a := Analyze(qs, agent, human)
		if len(perCode[a.Code]) >= target {
			continue
		}
		ids := make([]string, len(qs))
		for i, q := range qs {
			ids[i] = q.ID
		}
		sweet, worst := "", ""
		if a.Sweet != nil {
			sweet = a.Sweet.ID
		}
		if a.Worst != nil {
			worst = a.Worst.ID
		}
		perCode[a.Code] = append(perCode[a.Code], goldenCase{
			Name:        fmt.Sprintf("%s_%d", a.Code, len(perCode[a.Code])),
			QuestionIDs: ids,
			Agent:       agent,
			Human:       human,
			Expected: goldenExpected{
				Code: a.Code, Match: a.Match, Total: a.Total,
				EnergyBias: a.EnergyBias, PoliteGap: a.PoliteGap,
				PolarOpposite: a.PolarOpposite, HumanRarity: a.HumanRarity,
				KeyMiss: a.KeyMiss, AgentView: a.AgentView,
				SweetID: sweet, WorstID: worst,
			},
		})
	}

	for _, c := range want {
		if len(perCode[c]) < target {
			t.Fatalf("未能生成 code %s（仅 %d 个）——可达性不足", c, len(perCode[c]))
		}
	}

	var gf goldenFile
	for _, c := range want {
		gf.Cases = append(gf.Cases, perCode[c]...)
	}
	out, err := json.MarshalIndent(gf, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("testdata/golden.json", append(out, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %d golden cases covering all 10 codes", len(gf.Cases))
}
