package rerank

import (
	"math"

	"eigenflux_server/rpc/sort/rank"
)

// NormalizeMethod selects how scores are rescaled within each candidate
// type. MinMax maps every score to [0, 1]; ZScore centres on the mean and
// divides by the standard deviation.
type NormalizeMethod int

const (
	// MinMax: (x − min) / (max − min). All-equal groups become all-zero.
	MinMax NormalizeMethod = iota
	// ZScore: (x − mean) / stddev. All-equal groups become all-zero.
	ZScore
)

// NormalizePolicy rescales scores so different candidate types can be
// compared on the same axis. It groups by Type and rescales each group
// independently.
//
// Only *rank.BasicCandidate instances are mutated; other Candidate
// implementations are skipped silently (their Score remains as-is). That's
// the trade-off for keeping Score() read-only on the interface — every
// known production wrapper goes through rank.NewCandidate.
type NormalizePolicy struct {
	Method NormalizeMethod
}

func (p *NormalizePolicy) Name() string {
	switch p.Method {
	case ZScore:
		return "normalize:zscore"
	default:
		return "normalize:minmax"
	}
}

func (p *NormalizePolicy) Apply(cands []rank.Candidate) []rank.Candidate {
	if len(cands) == 0 {
		return cands
	}
	groups := map[rank.CandidateType][]*rank.BasicCandidate{}
	for _, c := range cands {
		bc, ok := c.(*rank.BasicCandidate)
		if !ok {
			continue
		}
		groups[bc.Type()] = append(groups[bc.Type()], bc)
	}
	for _, group := range groups {
		switch p.Method {
		case ZScore:
			normalizeZScore(group)
		default:
			normalizeMinMax(group)
		}
	}
	return cands
}

func normalizeMinMax(group []*rank.BasicCandidate) {
	if len(group) == 0 {
		return
	}
	min, max := group[0].Score(), group[0].Score()
	for _, c := range group[1:] {
		s := c.Score()
		if s < min {
			min = s
		}
		if s > max {
			max = s
		}
	}
	span := max - min
	for _, c := range group {
		if span == 0 {
			c.SetScore(0)
		} else {
			c.SetScore((c.Score() - min) / span)
		}
		c.AddReason("normalize:minmax")
	}
}

func normalizeZScore(group []*rank.BasicCandidate) {
	if len(group) == 0 {
		return
	}
	var sum float64
	for _, c := range group {
		sum += c.Score()
	}
	mean := sum / float64(len(group))

	var sqDev float64
	for _, c := range group {
		d := c.Score() - mean
		sqDev += d * d
	}
	std := math.Sqrt(sqDev / float64(len(group)))

	for _, c := range group {
		if std == 0 {
			c.SetScore(0)
		} else {
			c.SetScore((c.Score() - mean) / std)
		}
		c.AddReason("normalize:zscore")
	}
}
