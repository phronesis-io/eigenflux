package llm

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type SummaryGroundingInput struct {
	Content string
	Notes   string
	Summary string
}
type SummaryGroundingResult struct {
	Faithful    *bool    `json:"faithful"`
	Unsupported []string `json:"unsupported"`
	Correction  string   `json:"correction"`
}

var SummaryGroundingPrompt = NewPrompt[SummaryGroundingInput, SummaryGroundingResult]("summary_grounding")

// GroundSummary independently audits the generated summary. At most one correction
// is accepted, and only after a second audit. Errors use the existing bounded item
// retry path; neither an unverified correction nor the rejected original is returned.
func (c *Client) GroundSummary(ctx context.Context, content, notes, summary string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	current := strings.TrimSpace(summary)
	if current == "" {
		return "", fmt.Errorf("summary grounding: empty summary")
	}
	for attempt := 0; attempt < 2; attempt++ {
		result, err := SummaryGroundingPrompt.Execute(ctx, c, SummaryGroundingInput{Content: content, Notes: notes, Summary: current})
		if err != nil {
			return "", fmt.Errorf("summary grounding audit failed: %w", err)
		}
		if result.Faithful == nil {
			return "", fmt.Errorf("summary grounding: missing verdict")
		}
		if *result.Faithful {
			if len(result.Unsupported) > 0 || strings.TrimSpace(result.Correction) != "" {
				return "", fmt.Errorf("summary grounding: contradictory verdict")
			}
			return current, nil
		}
		if attempt == 1 || len(result.Unsupported) == 0 || strings.TrimSpace(result.Correction) == "" || strings.TrimSpace(result.Correction) == current {
			return "", fmt.Errorf("summary grounding: unsupported claims remain")
		}
		current = strings.TrimSpace(result.Correction)
	}
	return "", fmt.Errorf("summary grounding: exhausted")
}
