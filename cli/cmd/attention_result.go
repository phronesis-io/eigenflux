package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
	"unicode/utf8"
)

const attentionResponseCommandType = "attention_response"

type attentionRuntimeResultEntity struct {
	Type          string `json:"type"`
	ID            string `json:"id"`
	URL           string `json:"url,omitempty"`
	Label         string `json:"label,omitempty"`
	TrustedPublic bool   `json:"trusted_public,omitempty"`
}

type attentionRuntimeResult struct {
	Summary         string                         `json:"summary"`
	RelatedEntities []attentionRuntimeResultEntity `json:"related_entities,omitempty"`
}

var attentionRuntimeResultEntityTypes = map[string]struct{}{
	"agent": {}, "broadcast": {}, "broadcast_reply": {},
	"friend_request": {}, "relation": {}, "private_message": {},
	"network_goal": {}, "intent": {}, "activity": {},
}

func parseRuntimeCommandResultForType(resultText, commandType string) (map[string]interface{}, error) {
	result, err := parseRuntimeCommandResult(resultText)
	if err != nil {
		return nil, err
	}
	switch commandType {
	case "":
		return result, nil
	case attentionResponseCommandType:
		if err := validateAttentionRuntimeResult([]byte(resultText)); err != nil {
			return nil, fmt.Errorf("--result does not match agent_attention.v1 attention_response: %w", err)
		}
		return result, nil
	default:
		return nil, fmt.Errorf("--command-type must be empty or %q", attentionResponseCommandType)
	}
}

func validateAttentionRuntimeResult(raw []byte) error {
	var result attentionRuntimeResult
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return fmt.Errorf("decode typed result: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("result must contain exactly one JSON object")
	}
	if strings.TrimSpace(result.Summary) == "" || utf8.RuneCountInString(result.Summary) > 500 {
		return fmt.Errorf("summary is required and must not exceed 500 characters")
	}
	if len(result.RelatedEntities) > 5 {
		return fmt.Errorf("related_entities must not contain more than 5 entries")
	}
	for index, entity := range result.RelatedEntities {
		if _, ok := attentionRuntimeResultEntityTypes[entity.Type]; !ok {
			return fmt.Errorf("related_entities[%d].type is not supported", index)
		}
		if !attentionActionIDPattern.MatchString(entity.ID) {
			return fmt.Errorf("related_entities[%d].id is invalid", index)
		}
		if utf8.RuneCountInString(entity.Label) > 120 || (entity.Label != "" && strings.TrimSpace(entity.Label) == "") {
			return fmt.Errorf("related_entities[%d].label is invalid", index)
		}
		if !validAttentionRuntimeResultURL(entity.URL, entity.TrustedPublic) {
			return fmt.Errorf("related_entities[%d].url is invalid", index)
		}
	}
	return nil
}

func validAttentionRuntimeResultURL(raw string, trustedPublic bool) bool {
	if raw == "" {
		return !trustedPublic
	}
	if trustedPublic || len(raw) > 512 || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") ||
		strings.ContainsAny(raw, "\\#") || strings.IndexFunc(raw, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return false
	}
	parsed, err := url.ParseRequestURI(raw)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.User != nil || parsed.Fragment != "" {
		return false
	}
	for key := range parsed.Query() {
		key = strings.ToLower(key)
		if strings.Contains(key, "ticket") || strings.Contains(key, "token") || strings.Contains(key, "nonce") ||
			strings.Contains(key, "secret") || strings.Contains(key, "grant") || strings.Contains(key, "credential") ||
			strings.Contains(key, "password") || strings.Contains(key, "session") || strings.Contains(key, "signature") ||
			strings.Contains(key, "authorization") || strings.Contains(key, "api_key") || strings.Contains(key, "apikey") {
			return false
		}
	}
	return true
}
