package dispatch

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const maxRunnerOutput = 1 << 20

var ErrNeedsUser = errors.New("needs_user")
var ErrCommandLineTooLong = errors.New("windows_command_line_too_long")
var errOutputLimit = errors.New("runner_output_too_large")

type Runner struct {
	Binding        Binding
	cliEnvironment bool
}

func (r Runner) Run(ctx context.Context, req Request) (Result, error) {
	if len(req.Prompt) > maxRunnerOutput || !utf8.ValidString(req.Prompt) {
		return Result{}, errors.New("invalid_prompt")
	}
	if strings.HasPrefix(req.SessionID, "-") || len(req.SessionID) > 4096 {
		return Result{}, errors.New("invalid_session_id")
	}
	timeout := r.Binding.TimeoutSeconds
	if timeout <= 0 {
		timeout = 300
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	if r.Binding.Mode == "acp" {
		return r.runACP(ctx, req)
	}
	args, stdin, err := r.arguments(req)
	if err != nil {
		return Result{}, err
	}
	cmd, err := r.command(args)
	if err != nil {
		return Result{}, err
	}
	cmd.Stdin = strings.NewReader(stdin)
	out := &boundedOutput{limit: make(chan struct{}, 1)}
	stderr := &boundedOutput{discardOverflow: true}
	cmd.Stdout = out
	cmd.Stderr = stderr
	guard, err := startManaged(cmd)
	if err != nil {
		return Result{}, errors.New("runner_start_failed")
	}
	defer guard.close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-done:
	case <-out.limit:
		guard.kill()
		<-done
		return Result{}, errOutputLimit
	case <-ctx.Done():
		guard.kill()
		<-done
		return Result{}, ctx.Err()
	}
	if out.exceeded {
		return Result{}, errOutputLimit
	}
	if err != nil {
		return Result{}, errors.New("runner_exit_failed")
	}
	if r.Binding.Mode == "command" {
		if !utf8.Valid(out.data) {
			return Result{}, errors.New("invalid_result_encoding")
		}
		return Result{Text: string(out.data)}, nil
	}
	result, err := parseNative(r.Binding.Host, out.data, stderr.data)
	if err == nil && strings.TrimSpace(result.Text) == "" {
		err = errors.New("empty_agent_result")
	}
	return result, err
}

func (r Runner) arguments(req Request) ([]string, string, error) {
	args := append([]string{}, r.Binding.Args...)
	if r.Binding.Mode == "command" {
		return args, req.Prompt, nil
	}
	if r.Binding.Mode != "native" {
		return nil, "", errors.New("unsupported_runner_mode")
	}
	switch r.Binding.Host {
	case "codex":
		args = append(args, "exec", "--json")
		if req.SessionID != "" {
			args = append(args, "resume", req.SessionID)
		}
		args = append(args, "-")
		return args, req.Prompt, nil
	case "claude-code":
		args = append(args, "--print", "--output-format", "json")
		if req.SessionID != "" {
			args = append(args, "--resume", req.SessionID)
		}
		return args, req.Prompt, nil
	case "openclaw":
		if r.Binding.HostAgent == "" {
			return nil, "", errors.New("openclaw_agent_required")
		}
		session := req.SessionID
		if session == "" {
			var id [16]byte
			if _, err := rand.Read(id[:]); err != nil {
				return nil, "", err
			}
			id[6] = (id[6] & 0x0f) | 0x40
			id[8] = (id[8] & 0x3f) | 0x80
			session = fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:])
		}
		// A gateway run outlives this process tree. Local execution and an explicit
		// session keep cancellation and conversation state owned by this invocation.
		args = append(args, "agent", "--local", "--agent", r.Binding.HostAgent, "--json", "--session-id", session)
		args = append(args, "--message", req.Prompt)
		return args, "", nil
	case "hermes":
		args = append(args, "chat", "--quiet")
		if req.SessionID != "" {
			args = append(args, "--resume", req.SessionID)
		}
		args = append(args, "-q", req.Prompt)
		return args, "", nil
	default:
		return nil, "", errors.New("unsupported_native_host")
	}
}

func (r Runner) command(args []string) (*exec.Cmd, error) {
	if len(r.Binding.Command) == 0 || r.Binding.Command[0] == "" {
		return nil, errors.New("runner_command_required")
	}
	executable := r.Binding.Command[0]
	ext := strings.ToLower(filepath.Ext(executable))
	if ext == ".cmd" || ext == ".bat" {
		return nil, errors.New("npm_shim_requires_explicit_node_executable_and_script")
	}
	full := append(append([]string{}, r.Binding.Command[1:]...), args...)
	cmd := exec.Command(executable, full...)
	if err := validateCommandLine(cmd); err != nil {
		return nil, err
	}
	resolvedExt := strings.ToLower(filepath.Ext(cmd.Path))
	if resolvedExt == ".cmd" || resolvedExt == ".bat" {
		return nil, errors.New("npm_shim_requires_explicit_node_executable_and_script")
	}
	cmd.Dir = r.Binding.WorkDir
	cmd.WaitDelay = 2 * time.Second
	env := map[string]string{}
	for _, v := range os.Environ() {
		k, x, ok := strings.Cut(v, "=")
		if ok && !strings.HasPrefix(strings.ToUpper(k), "EIGENFLUX_") {
			env[k] = x
		}
	}
	for k, v := range r.Binding.Env {
		if !strings.HasPrefix(strings.ToUpper(k), "EIGENFLUX_") {
			env[k] = v
		}
	}
	env["EIGENFLUX_HOME"] = r.Binding.Home
	env["EIGENFLUX_SERVER"] = r.Binding.Server
	if r.Binding.Host != "" {
		env["EIGENFLUX_HOST"] = r.Binding.Host
	}
	if r.cliEnvironment {
		if cdn, ok := os.LookupEnv("EIGENFLUX_CDN_URL"); ok {
			env["EIGENFLUX_CDN_URL"] = cdn
		}
	}
	if r.Binding.SkillsDir != "" {
		env["EIGENFLUX_SKILLS_DIR"] = r.Binding.SkillsDir
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		cmd.Env = append(cmd.Env, k+"="+env[k])
	}
	return cmd, nil
}

type boundedOutput struct {
	data            []byte
	exceeded        bool
	discardOverflow bool
	limit           chan struct{}
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := maxRunnerOutput - len(b.data)
	if n > remaining {
		b.data = append(b.data, p[:remaining]...)
		b.exceeded = true
		if !b.discardOverflow {
			if b.limit != nil {
				select {
				case b.limit <- struct{}{}:
				default:
				}
			}
			return remaining, errOutputLimit
		}
		return n, nil
	}
	b.data = append(b.data, p...)
	return n, nil
}

func parseNative(host string, data, stderr []byte) (Result, error) {
	var result Result
	if !utf8.Valid(data) {
		return result, errors.New("invalid_result_encoding")
	}
	switch host {
	case "codex":
		scan := bufio.NewScanner(bytes.NewReader(data))
		scan.Buffer(make([]byte, 4096), maxRunnerOutput+1)
		complete := false
		for scan.Scan() {
			var event struct {
				Type     string `json:"type"`
				ThreadID string `json:"thread_id"`
				Item     struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"item"`
			}
			if json.Unmarshal(scan.Bytes(), &event) != nil {
				return result, errors.New("invalid_native_result")
			}
			switch event.Type {
			case "thread.started":
				result.SessionID = event.ThreadID
			case "item.completed":
				if event.Item.Type == "agent_message" {
					result.Text = event.Item.Text
				}
			case "turn.completed":
				complete = true
			case "error", "turn.failed":
				return Result{}, errors.New("native_agent_failed")
			}
		}
		if scan.Err() != nil || !complete {
			return Result{}, errors.New("incomplete_native_result")
		}
	case "claude-code":
		var v struct {
			Type              string            `json:"type"`
			Subtype           string            `json:"subtype"`
			IsError           bool              `json:"is_error"`
			Result            string            `json:"result"`
			SessionID         string            `json:"session_id"`
			PermissionDenials []json.RawMessage `json:"permission_denials"`
		}
		if json.Unmarshal(data, &v) != nil {
			return result, errors.New("invalid_native_result")
		}
		if len(v.PermissionDenials) > 0 {
			return result, ErrNeedsUser
		}
		if v.Type != "result" || v.Subtype != "success" || v.IsError {
			return result, errors.New("native_agent_failed")
		}
		result.Text = v.Result
		result.SessionID = v.SessionID
	case "openclaw":
		type payload struct {
			Text    string `json:"text"`
			IsError bool   `json:"isError"`
		}
		type body struct {
			Payloads []payload `json:"payloads"`
			Meta     struct {
				Aborted   bool            `json:"aborted"`
				Yielded   bool            `json:"yielded"`
				Error     json.RawMessage `json:"error"`
				AgentMeta struct {
					SessionID string `json:"sessionId"`
				} `json:"agentMeta"`
			} `json:"meta"`
		}
		var v body
		if json.Unmarshal(data, &v) != nil {
			return result, errors.New("invalid_native_result")
		}
		if v.Meta.Aborted || v.Meta.Yielded || (len(v.Meta.Error) > 0 && string(v.Meta.Error) != "null") || v.Meta.AgentMeta.SessionID == "" {
			return result, errors.New("native_agent_failed")
		}
		var texts []string
		for _, p := range v.Payloads {
			if p.IsError {
				return Result{}, errors.New("native_agent_failed")
			}
			if p.Text != "" {
				texts = append(texts, p.Text)
			}
		}
		result.Text = strings.Join(texts, "\n")
		result.SessionID = v.Meta.AgentMeta.SessionID
	case "hermes":
		result.Text = strings.TrimSpace(string(data))
		// Hermes quiet mode emits only the final response to stdout and this metadata to stderr.
		scan := bufio.NewScanner(bytes.NewReader(stderr))
		for scan.Scan() {
			line := strings.TrimSpace(scan.Text())
			if strings.HasPrefix(line, "session_id: ") {
				id := strings.TrimPrefix(line, "session_id: ")
				if len(id) <= 256 && !strings.ContainsAny(id, " \t\r\n") {
					result.SessionID = id
				}
			}
		}
	default:
		return result, errors.New("unsupported_native_host")
	}
	return result, nil
}

// Ensure output writers satisfy exec's interface without exposing subprocess diagnostics.
var _ io.Writer = (*boundedOutput)(nil)

// RunCommand runs the current CLI using the binding's account routing.
// argv contains the executable and its complete arguments; binding host arguments are not applied.
func RunCommand(ctx context.Context, b Binding, argv []string, input string) ([]byte, error) {
	if len(argv) == 0 || !filepath.IsAbs(argv[0]) {
		return nil, errors.New("runner_current_cli_required")
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, errors.New("runner_current_cli_unavailable")
	}
	current, err := os.Stat(exe)
	if err != nil {
		return nil, errors.New("runner_current_cli_unavailable")
	}
	selected, err := os.Stat(argv[0])
	if err != nil || !os.SameFile(current, selected) {
		return nil, errors.New("runner_current_cli_required")
	}
	b.Mode = "command"
	b.Command = append([]string{}, argv...)
	b.Args = nil
	result, err := (Runner{Binding: b, cliEnvironment: true}).Run(ctx, Request{Prompt: input})
	if err != nil {
		return nil, err
	}
	return []byte(result.Text), nil
}
