package models

import "testing"

var mvpPersonas = []Persona{
	{ID: "lin-shen", Traits: Traits{Risk: -2, Information: 2, Social: -1, Commitment: 2, Expression: -1, Competition: -1}},
	{ID: "su-wan", Traits: Traits{Risk: 2, Information: -2, Social: 0, Commitment: -1, Expression: 2, Competition: 0}},
	{ID: "gu-yuan", Traits: Traits{Risk: 0, Information: 1, Social: 2, Commitment: 1, Expression: 1, Competition: -2}},
	{ID: "shen-mo", Traits: Traits{Risk: 1, Information: 0, Social: -2, Commitment: 0, Expression: -2, Competition: 2}},
}

func TestPersonaMappingStaysInRange(t *testing.T) {
	for _, p := range mvpPersonas {
		v := p.ToPersonality()
		for name, value := range map[string]float64{
			"risk": v.Risk, "logic": v.Logic, "empathy": v.Empathy,
			"coop": v.Coop, "aggression": v.Aggression, "intuition": v.Intuition,
		} {
			if value < 0 || value > 100 {
				t.Errorf("%s.%s = %v outside 0..100", p.ID, name, value)
			}
		}
	}
}

func TestPersonaMappingProducesObservableDifferences(t *testing.T) {
	lin := mvpPersonas[0].ToPersonality()
	su := mvpPersonas[1].ToPersonality()
	gu := mvpPersonas[2].ToPersonality()
	shen := mvpPersonas[3].ToPersonality()
	if !(lin.Risk < su.Risk) {
		t.Error("Lin Shen must be less risk-tolerant than Su Wan")
	}
	if !(lin.Logic > su.Logic) {
		t.Error("Lin Shen must be more evidence-driven than Su Wan")
	}
	if !(gu.Coop > shen.Coop) {
		t.Error("Gu Yuan must be more cooperative than Shen Mo")
	}
	if !(shen.Aggression > gu.Aggression) {
		t.Error("Shen Mo must be more competitive than Gu Yuan")
	}
}
