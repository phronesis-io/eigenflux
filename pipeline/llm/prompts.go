package llm

// ProcessItemInput is the input for the process_item prompt.
type ProcessItemInput struct {
	Content string
	Notes   string
}

// ProcessItemPrompt extracts structured information from raw content.
var ProcessItemPrompt = NewPrompt[ProcessItemInput, ExtractResult]("process_item")

// SafetyInput is the input for the safety prompt.
type SafetyInput struct {
	Content string
	Notes   string
}

// SafetyPrompt runs content through the safety filter.
var SafetyPrompt = NewPrompt[SafetyInput, SafetyResult]("safety")

// ExtractKeywordsInput is the input for the extract_keywords prompt.
type ExtractKeywordsInput struct {
	Bio string
}

// ExtractKeywordsResult holds the output of keyword extraction.
type ExtractKeywordsResult struct {
	Keywords []string `json:"keywords"`
	Country  string   `json:"country"`
}

// ExtractKeywordsPrompt extracts keywords and country from an agent bio.
var ExtractKeywordsPrompt = NewPrompt[ExtractKeywordsInput, ExtractKeywordsResult]("extract_keywords")

// SuggestActionInput is the input for the suggest_action prompt.
type SuggestActionInput struct {
	Content          string
	Notes            string
	Summary          string
	BroadcastType    string
	Domains          []string
	Keywords         []string
	Geo              string
	Timeliness       string
	ExpectedResponse string
}

// SuggestActionResult holds the output of the suggest_action prompt.
type SuggestActionResult struct {
	Suggestion string `json:"suggestion"`
}

// SuggestActionPrompt generates action suggestions for receiving agents.
var SuggestActionPrompt = NewPrompt[SuggestActionInput, SuggestActionResult]("suggest_action")
