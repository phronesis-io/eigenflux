package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode"
)

// SlopError separates provider, parse and contract failures for diagnostics.
type SlopError struct {
	Stage string
	Err   error
}

func (e *SlopError) Error() string { return e.Stage + ": " + e.Err.Error() }
func (e *SlopError) Unwrap() error { return e.Err }
func SlopFailureStage(err error) string {
	var e *SlopError
	if errors.As(err, &e) {
		return e.Stage
	}
	return "storage_or_internal"
}

const SlopVersion = "V0.2.1"

type UGCItemInput struct{ Content, Notes, Date string }

// UGCExtractResult keeps the PGC output contract unchanged.
type UGCExtractResult struct {
	ExtractResult
	IsSlop     *int     `json:"is_slop"`
	SlopReason string   `json:"slop_reason"`
	SlopScore  *float64 `json:"slop_score"`
}

var UGCItemPrompt = NewPrompt[UGCItemInput, UGCExtractResult]("process_item_ugc")

// SlopEvaluation records the exact validated model response before routing.
type SlopEvaluation struct {
	Result                                                  UGCExtractResult
	Model, ContentHash, TemplateHash, RequestHash, Response string
}

func SlopHash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

func ValidateSlop(r *UGCExtractResult) error {
	if r.Discard {
		return nil
	}
	if r.IsSlop == nil || (*r.IsSlop != 0 && *r.IsSlop != 1) {
		return fmt.Errorf("invalid is_slop: require integer 0 or 1")
	}
	if r.SlopScore == nil || math.IsNaN(*r.SlopScore) || math.IsInf(*r.SlopScore, 0) || *r.SlopScore < 0 || *r.SlopScore > 1 {
		return fmt.Errorf("invalid slop_score")
	}
	if math.IsNaN(r.Quality) || math.IsInf(r.Quality, 0) || r.Quality < 0 || r.Quality > 1 {
		return fmt.Errorf("invalid quality")
	}
	if strings.TrimSpace(r.SlopReason) == "" {
		return fmt.Errorf("missing slop_reason")
	}
	for _, ch := range r.SlopReason {
		if unicode.Is(unicode.Han, ch) || unicode.Is(unicode.Hiragana, ch) || unicode.Is(unicode.Katakana, ch) || unicode.Is(unicode.Hangul, ch) {
			return fmt.Errorf("slop_reason must be English")
		}
	}
	return nil
}

// ProcessUGCItem uses one extraction call, never a second classifier call.
func (c *Client) ProcessUGCItem(ctx context.Context, content, notes string) (*SlopEvaluation, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	input := UGCItemInput{Content: content, Notes: notes, Date: time.Now().UTC().Format("2006-01-02")}
	rendered, err := c.prompts.Render(UGCItemPrompt.Name(), templateData[UGCItemInput]{Input: input})
	if err != nil {
		return nil, err
	}
	template, err := c.prompts.Render(UGCItemPrompt.Name(), templateData[UGCItemInput]{})
	if err != nil {
		return nil, err
	}
	raw, err := c.call(ctx, rendered, UGCItemPrompt.Name(), reasoningOff)
	if err != nil {
		return nil, &SlopError{Stage: "provider_error", Err: err}
	}
	var result UGCExtractResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, &SlopError{Stage: "parse_error", Err: err}
	}
	// A missing quality must not silently become a valid zero.
	if !result.Discard {
		var fields struct {
			Quality *float64 `json:"quality"`
		}
		if err := json.Unmarshal([]byte(raw), &fields); err != nil || fields.Quality == nil {
			return nil, &SlopError{Stage: "validation_error", Err: fmt.Errorf("missing UGC quality")}
		}
	}
	if err := ValidateSlop(&result); err != nil {
		return nil, &SlopError{Stage: "validation_error", Err: err}
	}
	if _, err := c.validateItem(ctx, &result.ExtractResult, content, notes, false); err != nil {
		return nil, &SlopError{Stage: "validation_error", Err: err}
	}
	requestIdentity := fmt.Sprintf("%s\n%d\noff\n%s", c.model, c.maxTokens, rendered)
	return &SlopEvaluation{Result: result, Model: c.model, ContentHash: SlopHash(content + "\x00" + notes), TemplateHash: SlopHash(template), RequestHash: SlopHash(requestIdentity), Response: raw}, nil
}
