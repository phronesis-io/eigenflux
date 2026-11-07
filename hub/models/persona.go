package models

// Persona is the GDD's display/identity layer. Its six traits are on a -2..+2
// scale (design/game-brief.md "Persona storage format"). This is NOT the same
// six-dimension vector the frozen prototype's decision engine uses (0..100:
// risk/logic/empathy/coop/aggression/intuition). ToPersonality is the explicit
// bridge. See docs/architecture/fluxspace-contracts.md, "Dimension mismatch".
type Persona struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Occupation string `json:"occupation"`
	Tagline    string `json:"tagline"`
	Traits     Traits `json:"traits"`
}

type Traits struct {
	Risk        int `json:"risk"`
	Information int `json:"information"`
	Social      int `json:"social"`
	Commitment  int `json:"commitment"`
	Expression  int `json:"expression"`
	Competition int `json:"competition"`
}

// ToPersonality maps GDD traits onto the engine vector. The mapping is a
// documented MVP approximation, not a claim of equivalence: the two systems
// describe different axes. Every output is clamped to 0..100.
func (p Persona) ToPersonality() Personality {
	return Personality{
		Risk:       scale(50 + 25*p.Traits.Risk),
		Logic:      scale(50 + 25*p.Traits.Information),
		Coop:       scale(50 + 20*p.Traits.Social + 5*p.Traits.Commitment),
		Aggression: scale(50 + 25*p.Traits.Competition),
		Intuition:  scale(50 + 20*p.Traits.Expression - 5*p.Traits.Information),
		Empathy:    scale(50 + 15*p.Traits.Social + 10*p.Traits.Expression),
	}
}

func scale(v int) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return float64(v)
}
