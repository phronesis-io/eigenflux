package discovery

import "slices"

// SourceLimit freezes an operator-configured fraction for recommendation delivery.
// Keeping the ratio allows load_more to use a different page size safely.
type SourceLimit struct {
	Source      string `json:"source"`
	Numerator   int    `json:"numerator"`
	Denominator int    `json:"denominator"`
}

// SelectRecommendationPage keeps ranked order and backfills capped sources from
// the remaining pool. Overflow stays available for the next legacy Feed page.
func SelectRecommendationPage(in []Candidate, limit int, rules []SourceLimit) (selected, remaining []Candidate) {
	counts := make([]int, len(rules))
	for _, c := range in {
		fits := len(selected) < limit
		for i, rule := range rules {
			if c.Document.Ref.Type == Broadcast && slices.Contains(c.Document.Channels, rule.Source) &&
				(rule.Denominator <= 0 || counts[i] >= limit*rule.Numerator/rule.Denominator) {
				fits = false
			}
		}
		if !fits {
			remaining = append(remaining, c)
			continue
		}
		selected = append(selected, c)
		for i, rule := range rules {
			if c.Document.Ref.Type == Broadcast && slices.Contains(c.Document.Channels, rule.Source) {
				counts[i]++
			}
		}
	}
	return selected, remaining
}
