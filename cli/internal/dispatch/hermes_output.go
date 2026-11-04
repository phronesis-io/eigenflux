package dispatch

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

var hermesFormatChoices = regexp.MustCompile(`--format\s+\{([^}]*)\}`)

// Probe the actual bound executable, argv prefix, environment and cwd without a
// query. A failed probe never authorizes a model retry or a legacy fallback.
func (r Runner) probeHermesStreamJSON(ctx context.Context) (bool, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	args := append(append([]string{}, r.Binding.Args...), "chat", "--help")
	out, _, err := r.runProcess(probeCtx, args, "")
	if err != nil {
		code := "hermes_capability_probe_failed"
		if errors.Is(err, context.DeadlineExceeded) {
			code = "hermes_capability_probe_timed_out"
		}
		return false, &DiagnosticError{Cause: err, Code: code}
	}
	if !utf8.Valid(out) {
		return false, errors.New("hermes_capability_probe_failed")
	}
	if match := hermesFormatChoices.FindSubmatch(out); len(match) == 2 {
		for _, choice := range strings.Split(string(match[1]), ",") {
			if strings.TrimSpace(choice) == "stream-json" {
				return true, nil
			}
		}
	}
	return false, nil
}

// Hermes' formal JSONL protocol is init, intermediate events, then one terminal
// result. Tool previews and text deltas are never business decisions.
func parseHermesStreamJSON(data []byte) (Result, error) {
	var result Result
	if len(data) > maxRunnerOutput || !utf8.Valid(data) {
		return result, errors.New("invalid_native_result")
	}
	scan := bufio.NewScanner(bytes.NewReader(data))
	scan.Buffer(make([]byte, 4096), maxRunnerOutput+1)
	initialized, completed, initialSession := false, false, ""
	for scan.Scan() {
		line := bytes.TrimSpace(scan.Bytes())
		if len(line) == 0 {
			continue
		}
		var fields map[string]json.RawMessage
		if completed || decodeStrictObject(line, &fields) != nil || fields["type"] == nil {
			return Result{}, errors.New("invalid_native_result")
		}
		var event struct {
			Type      string          `json:"type"`
			Subtype   string          `json:"subtype"`
			SessionID string          `json:"session_id"`
			Text      *string         `json:"text"`
			ExitCode  *int            `json:"exit_code"`
			Error     json.RawMessage `json:"error"`
		}
		if json.Unmarshal(line, &event) != nil {
			return Result{}, errors.New("invalid_native_result")
		}
		if !initialized {
			if event.Type != "system" || event.Subtype != "init" || fields["subtype"] == nil || event.SessionID != "" && !validACPMainID(event.SessionID) {
				return Result{}, errors.New("invalid_native_result")
			}
			initialized, initialSession = true, event.SessionID
			continue
		}
		switch event.Type {
		case "text", "tool_use", "tool_result":
			// A tool failure can be corrected within this same model invocation.
		case "result":
			if event.ExitCode == nil || event.Text == nil || fields["exit_code"] == nil || fields["session_id"] == nil || fields["text"] == nil || !validACPMainID(event.SessionID) || initialSession != "" && initialSession != event.SessionID {
				return Result{}, errors.New("invalid_native_result")
			}
			problem := bytes.TrimSpace(event.Error)
			if *event.ExitCode != 0 || len(problem) > 0 && !bytes.Equal(problem, []byte("null")) && !bytes.Equal(problem, []byte(`""`)) {
				return Result{}, errors.New("native_agent_failed")
			}
			result.Text, result.SessionID, completed = *event.Text, event.SessionID, true
		default:
			return Result{}, errors.New("invalid_native_result")
		}
	}
	if scan.Err() != nil || !completed {
		return Result{}, errors.New("incomplete_native_result")
	}
	if len(bytes.TrimSpace([]byte(result.Text))) == 0 {
		return Result{}, errors.New("empty_agent_result")
	}
	return result, nil
}
