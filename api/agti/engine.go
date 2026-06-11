package agti

import "sort"

// This file is a faithful port of the demo's lib/engine.js. The rule cascade,
// tie-breaking and copy must stay byte-identical to the JS version so existing
// tuning carries over — change both together or bump the bank copy instead.

// PerQuestion is the per-question comparison detail.
type PerQuestion struct {
	ID    string
	Text  string
	Hit   bool
	Agent *Option
	Human *Option
	Gap   int
	Key   bool
}

// Analysis is the engine output for one session.
type Analysis struct {
	Code          string
	Match         int
	Total         int
	EnergyBias    int
	PoliteGap     int
	PolarOpposite int
	HumanRarity   int
	KeyMiss       bool
	AgentView     string
	Sweet         *PerQuestion
	Worst         *PerQuestion
	PerQ          []PerQuestion
}

func optOf(q *Question, key string) *Option {
	for i := range q.Options {
		if q.Options[i].Key == key {
			return &q.Options[i]
		}
	}
	return nil
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// Analyze compares the agent's and the human's answers over the session's
// questions and maps the signals to a relationship type code.
func Analyze(questions []Question, agentAns, humanAns map[string]string) Analysis {
	var (
		match         int
		energyAgent   int
		energyHuman   int
		politeGap     int // human picked the polite option, agent didn't → 照妖镜 signal
		polarOpposite int // energy gap >= 3 on a miss → strong contrast
		humanRarity   int // human picked a rare option
		keyMiss       bool
	)
	perQ := make([]PerQuestion, 0, len(questions))

	for i := range questions {
		q := &questions[i]
		aKey, hKey := agentAns[q.ID], humanAns[q.ID]
		aOpt, hOpt := optOf(q, aKey), optOf(q, hKey)
		ae, he := 0, 0
		if aOpt != nil {
			ae = aOpt.Energy
		}
		if hOpt != nil {
			he = hOpt.Energy
		}
		hit := aKey != "" && hKey != "" && aKey == hKey
		if hit {
			match++
		}
		energyAgent += ae
		energyHuman += he
		if !hit && hOpt != nil && hOpt.Polite && aOpt != nil && !aOpt.Polite {
			politeGap++
		}
		if !hit && abs(ae-he) >= 3 {
			polarOpposite++
		}
		if hOpt != nil && hOpt.Rare {
			humanRarity++
		}
		if q.Key && !hit {
			keyMiss = true
		}
		perQ = append(perQ, PerQuestion{ID: q.ID, Text: q.Text, Hit: hit, Agent: aOpt, Human: hOpt, Gap: abs(ae - he), Key: q.Key})
	}

	n := len(questions)
	if n == 0 {
		n = 1
	}
	energyBias := energyAgent - energyHuman // >0: agent sees the human as bolder than they are
	bothBold := float64(energyHuman)/float64(n) >= 0.6 && float64(energyAgent)/float64(n) >= 0.6

	// Rule cascade, priority top to bottom.
	var code string
	switch {
	case match >= 9:
		code = "SOUL"
	case match >= 7 && bothBold:
		code = "GANG"
	case politeGap >= 2 && match <= 7:
		code = "XRAY"
	case keyMiss && match >= 7:
		code = "NPC"
	case energyBias >= 4:
		code = "KISS"
	case match >= 7 && humanRarity == 0:
		code = "CTRL"
	case humanRarity >= 3:
		code = "RIDDLE"
	case match <= 4 && polarOpposite >= 3:
		code = "VS"
	case energyBias <= -4:
		code = "MAMA"
	case match <= 4:
		code = "FAKE"
	default:
		code = "MAMA" // middle ground: it reads you as a bit steadier than you are
	}

	// Sweetest hit: prefer key questions, otherwise first hit in question order
	// (matches the JS stable sort by key desc).
	var hits, misses []PerQuestion
	for _, p := range perQ {
		if p.Hit {
			hits = append(hits, p)
		} else {
			misses = append(misses, p)
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Key && !hits[j].Key })
	// Worst miss: largest energy gap, key questions break ties.
	sort.SliceStable(misses, func(i, j int) bool {
		if misses[i].Gap != misses[j].Gap {
			return misses[i].Gap > misses[j].Gap
		}
		return misses[i].Key && !misses[j].Key
	})
	var sweet, worst *PerQuestion
	if len(hits) > 0 {
		sweet = &hits[0]
	}
	if len(misses) > 0 {
		worst = &misses[0]
	}

	return Analysis{
		Code: code, Match: match, Total: n,
		EnergyBias: energyBias, PoliteGap: politeGap, PolarOpposite: polarOpposite,
		HumanRarity: humanRarity, KeyMiss: keyMiss,
		AgentView: agentView(energyAgent, n, energyBias),
		Sweet:     sweet, Worst: worst, PerQ: perQ,
	}
}

func agentView(energyAgent, n, bias int) string {
	avg := float64(energyAgent) / float64(n)
	var core string
	switch {
	case avg >= 0.8:
		core = "在它眼里，你是个上头就冲、闲不住的行动派"
	case avg >= 0.2:
		core = "在它眼里，你外向里带点分寸，能浪也能收"
	case avg >= -0.4:
		core = "在它眼里，你低调务实、怎么舒服怎么来"
	default:
		core = "在它眼里，你是个佛系慢热、自己待着最香的人"
	}
	var tail string
	switch {
	case bias >= 4:
		tail = "——而且它明显把你想得比你自己更闪。"
	case bias <= -4:
		tail = "——而且它总把你往「稳一点」的方向猜。"
	default:
		tail = "。"
	}
	return core + tail
}
