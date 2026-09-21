// Package dispatch connects account-scoped watch events to explicitly bound local Agents.
package dispatch

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

type Binding struct {
	Version        int               `json:"version"`
	Home           string            `json:"home"`
	Server         string            `json:"server"`
	Endpoint       string            `json:"endpoint"`
	AgentID        string            `json:"agent_id"`
	PrincipalID    string            `json:"principal_id"`
	Scope          string            `json:"scope"`
	Revision       string            `json:"revision"`
	Mode           string            `json:"mode"`
	Host           string            `json:"host"`
	Command        []string          `json:"command"`
	Args           []string          `json:"args,omitempty"`
	WorkDir        string            `json:"workdir"`
	SkillsDir      string            `json:"skills_dir,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	HostAgent      string            `json:"host_agent,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds"`
	// PM replies are always sent by the CLI in this runner. Existing plugin mode is separate.
	Events []string `json:"events"`
}

type Message struct {
	ID           string `json:"msg_id"`
	Conversation string `json:"conv_id"`
	Sender       string `json:"sender_id"`
	Receiver     string `json:"receiver_id"`
	Content      string `json:"content"`
	CreatedAt    int64  `json:"created_at,omitempty"`
}

type Job struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	Scope     string          `json:"scope"`
	Revision  string          `json:"binding_revision"`
	Message   *Message        `json:"message,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	Status    string          `json:"status"`
	Code      string          `json:"code,omitempty"`
	Created   int64           `json:"created"`
	SessionID string          `json:"session_id,omitempty"`
	ReplyID   string          `json:"reply_id,omitempty"`
}

type Request struct {
	ID        string
	Kind      string
	Prompt    string
	SessionID string
}

type Result struct {
	Text      string
	SessionID string
	RunID     string
}

// Decision is a business result inside a model response, not an ACP extension.
type Decision struct {
	Version   int    `json:"version"`
	RequestID string `json:"request_id"`
	Action    string `json:"action"`
	ReplyText string `json:"reply_text"`
}

func ParseDecision(raw, requestID string) (Decision, error) {
	var d Decision
	if len(raw) > 1<<20 || !utf8.ValidString(raw) {
		return d, errors.New("invalid_result_encoding_or_size")
	}
	if err := decodeStrictObject([]byte(raw), &d); err != nil {
		return d, errors.New("invalid_result_contract")
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal([]byte(raw), &fields)
	for _, key := range []string{"version", "request_id", "action", "reply_text"} {
		if value, ok := fields[key]; !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return d, errors.New("missing_result_field")
		}
	}
	if strings.TrimSpace(requestID) == "" || d.Version != 1 || d.RequestID != requestID {
		return d, errors.New("result_identity_mismatch")
	}
	switch d.Action {
	case "reply":
		if strings.TrimSpace(d.ReplyText) == "" || strings.ContainsRune(d.ReplyText, utf8.RuneError) {
			return d, errors.New("invalid_reply")
		}
	case "no_reply", "needs_user":
		if d.ReplyText != "" {
			return d, errors.New("unexpected_reply_text")
		}
	default:
		return d, errors.New("invalid_result_action")
	}
	return d, nil
}

// decodeStrictObject rejects duplicate keys before decoding so identity and action
// fields cannot have different meanings to different JSON consumers.
func decodeStrictObject(raw []byte, value any) error {
	if !utf8.Valid(raw) {
		return errors.New("invalid JSON encoding")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	first, err := dec.Token()
	if err != nil || first != json.Delim('{') {
		return errors.New("expected JSON object")
	}
	if err := checkJSONContainer(dec, json.Delim('{')); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("expected one JSON object")
	}
	dec = json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(value)
}

func checkJSONContainer(dec *json.Decoder, opening json.Delim) error {
	keys := map[string]bool{}
	for dec.More() {
		if opening == '{' {
			key, err := dec.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok {
				return errors.New("invalid JSON key")
			}
			folded := strings.ToUpper(name)
			if keys[folded] {
				return errors.New("duplicate JSON key")
			}
			keys[folded] = true
		}
		token, err := dec.Token()
		if err != nil {
			return err
		}
		if delim, ok := token.(json.Delim); ok {
			if delim != '{' && delim != '[' {
				return errors.New("invalid JSON container")
			}
			if err := checkJSONContainer(dec, delim); err != nil {
				return err
			}
		}
	}
	closing, err := dec.Token()
	if err != nil {
		return err
	}
	expected := json.Delim('}')
	if opening == '[' {
		expected = ']'
	}
	if closing != expected {
		return errors.New("invalid JSON container")
	}
	return nil
}
