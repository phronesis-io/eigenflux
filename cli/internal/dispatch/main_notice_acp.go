package dispatch

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// ACP has no standard hidden-input operation. This negotiated extension is
// implemented by a bridge attached to the existing host owner, not by spawning
// another Agent with session/load. Never fall back to session/prompt or cancel.
const mainConversationExtension = "ai.eigenflux.mainConversation"

// Main-only bridges need not implement fresh-session execution. Keep seller
// inspection/fulfillment and PM decisions on the original command and mode.
func acpMainBinding(binding Binding) Binding {
	if len(binding.MainCommand) != 0 {
		binding.Mode, binding.Command, binding.Args = "acp", append([]string(nil), binding.MainCommand...), nil
	}
	return binding
}

type acpMainCapability struct {
	Version         int    `json:"version"`
	Host            string `json:"host"`
	ExistingSession bool   `json:"existingSession"`
	HiddenInput     bool   `json:"hiddenInput"`
	ResultOnly      bool   `json:"resultOnly"`
}

type acpMainInspection struct {
	SessionID       string `json:"sessionId"`
	Host            string `json:"host"`
	Cwd             string `json:"cwd"`
	State           string `json:"state"`
	ExistingSession bool   `json:"existingSession"`
	HiddenInput     bool   `json:"hiddenInput"`
	ResultOnly      bool   `json:"resultOnly"`
}

func validACPMainID(id string) bool {
	if strings.TrimSpace(id) != id || id == "" || len(id) > 4096 || strings.HasPrefix(id, "-") || !utf8.ValidString(id) {
		return false
	}
	for _, r := range id {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}

func (r Runner) inspectACPMain(c *acpClient) (acpMainInspection, error) {
	var target acpMainInspection
	c.backgroundOnly = true
	raw, err := c.call("initialize", map[string]any{
		"protocolVersion":    1,
		"clientCapabilities": map[string]any{"_meta": map[string]any{mainConversationExtension: map[string]int{"version": 1}}},
		"clientInfo":         map[string]string{"name": "eigenflux", "version": "1"},
	}, false)
	if err != nil {
		return target, err
	}
	var initialized struct {
		ProtocolVersion   int `json:"protocolVersion"`
		AgentCapabilities struct {
			Meta map[string]json.RawMessage `json:"_meta"`
		} `json:"agentCapabilities"`
	}
	var capability acpMainCapability
	var envelope map[string]json.RawMessage
	if decodeStrictObject(raw, &envelope) != nil || json.Unmarshal(raw, &initialized) != nil || initialized.ProtocolVersion != 1 ||
		decodeStrictObject(initialized.AgentCapabilities.Meta[mainConversationExtension], &capability) != nil ||
		capability.Version != 1 || capability.Host != r.Binding.Host || !capability.ExistingSession || !capability.HiddenInput || !capability.ResultOnly {
		return target, ErrMainNoticeUnsupported
	}
	raw, err = c.call("_eigenflux/main/inspect", map[string]string{"sessionId": r.Binding.MainSessionID, "cwd": r.Binding.WorkDir}, false)
	if err != nil {
		return target, err
	}
	if decodeStrictObject(raw, &target) != nil || target.SessionID != r.Binding.MainSessionID || target.Host != r.Binding.Host ||
		target.Cwd != r.Binding.WorkDir || !filepath.IsAbs(target.Cwd) || !target.ExistingSession || !target.HiddenInput || !target.ResultOnly {
		return acpMainInspection{}, ErrMainSessionMismatch
	}
	if target.State == "busy" {
		return target, ErrMainNoticeBusy
	}
	if target.State != "idle" {
		return target, ErrMainSessionUnavailable
	}
	return target, nil
}

func (r Runner) runACPMainNotice(c *acpClient, prompt string) (Result, error) {
	if _, err := r.inspectACPMain(c); err != nil {
		return Result{}, err
	}
	id, err := NewRevision()
	if err != nil {
		return Result{}, err
	}
	raw, err := c.call("_eigenflux/main/notify", map[string]string{
		"sessionId": r.Binding.MainSessionID, "cwd": r.Binding.WorkDir, "requestId": id, "prompt": prompt,
	}, false)
	if err != nil {
		return Result{}, err
	}
	var receipt struct {
		SessionID       string `json:"sessionId"`
		RequestID       string `json:"requestId"`
		RunID           string `json:"runId"`
		StopReason      string `json:"stopReason"`
		Text            string `json:"text"`
		ExistingSession bool   `json:"existingSession"`
		HiddenInput     bool   `json:"hiddenInput"`
		ResultOnly      bool   `json:"resultOnly"`
	}
	if decodeStrictObject(raw, &receipt) != nil || receipt.SessionID != r.Binding.MainSessionID || receipt.RequestID != id ||
		!validACPMainID(receipt.RunID) || receipt.StopReason != "end_turn" || !receipt.ExistingSession || !receipt.HiddenInput || !receipt.ResultOnly ||
		strings.TrimSpace(receipt.Text) == "" || !utf8.ValidString(receipt.Text) || len(receipt.Text) > maxRunnerOutput {
		return Result{}, errors.New("main_notice_response_unconfirmed")
	}
	return Result{Text: receipt.Text, SessionID: receipt.SessionID, RunID: receipt.RunID}, nil
}
