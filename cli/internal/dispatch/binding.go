package dispatch

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	watchstate "cli.eigenflux.ai/internal/watch"
)

func BindingPath(home, server string) string {
	return filepath.Join(home, "watch", watchstate.Scope(home, server, "", "")+".binding.json")
}

const maxBindingBytes = 64 << 10

func DecodeBinding(r io.Reader) (Binding, error) {
	var b Binding
	raw, err := io.ReadAll(io.LimitReader(r, maxBindingBytes+1))
	if err != nil {
		return b, fmt.Errorf("read binding: %w", err)
	}
	if len(raw) > maxBindingBytes {
		return b, errors.New("binding exceeds size limit")
	}
	if err := decodeStrictObject(raw, &b); err != nil {
		return b, fmt.Errorf("invalid binding: %w", err)
	}
	return b, nil
}

func ReadBinding(home, server string) (Binding, error) {
	f, err := os.Open(BindingPath(home, server))
	if err != nil {
		return Binding{}, err
	}
	defer f.Close()
	b, err := DecodeBinding(f)
	if err != nil {
		return Binding{}, err
	}
	if b.Home != home || b.Server != server {
		return Binding{}, errors.New("binding path identity mismatch")
	}
	if err := b.validateIdentity(); err != nil {
		return Binding{}, err
	}
	return b, nil
}

func (b Binding) validateIdentity() error {
	for _, value := range []string{b.Home, b.Server, b.Endpoint, b.AgentID, b.PrincipalID} {
		if strings.TrimSpace(value) == "" || strings.ContainsRune(value, 0) {
			return errors.New("binding identity mismatch; bind the current account again")
		}
	}
	if b.Version != 1 || !filepath.IsAbs(b.Home) || b.Scope != watchstate.Scope(b.Home, b.Server, b.AgentID, b.PrincipalID) {
		return errors.New("binding identity mismatch; bind the current account again")
	}
	revision, err := hex.DecodeString(b.Revision)
	if err != nil || len(b.Revision) != 32 || len(revision) != 16 {
		return errors.New("binding revision must be 32 hexadecimal characters")
	}
	endpoint, err := url.Parse(b.Endpoint)
	if err != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return errors.New("binding endpoint must be an absolute HTTP URL")
	}
	return nil
}

func (b *Binding) Validate() error {
	if err := b.validateIdentity(); err != nil {
		return err
	}
	if b.SkillsDir != "" && (!filepath.IsAbs(b.SkillsDir) || strings.ContainsRune(b.SkillsDir, 0)) {
		return errors.New("skills_dir must be absolute")
	}
	if !filepath.IsAbs(b.WorkDir) {
		return errors.New("workdir must be absolute")
	}
	info, err := os.Stat(b.WorkDir)
	if err != nil || !info.IsDir() {
		return errors.New("workdir unavailable")
	}
	b.WorkDir, err = filepath.EvalSymlinks(b.WorkDir)
	if err != nil {
		return err
	}
	if b.TimeoutSeconds == 0 {
		b.TimeoutSeconds = 600
	}
	if b.TimeoutSeconds < 1 || b.TimeoutSeconds > 3600 {
		return errors.New("timeout_seconds must be 1..3600")
	}
	if b.Mode == "" {
		b.Mode = "native"
	}
	switch b.Mode {
	case "native":
		switch b.Host {
		case "codex", "claude-code", "hermes":
		case "openclaw":
			if b.HostAgent == "" {
				return errors.New("openclaw requires host_agent")
			}
		default:
			return errors.New("no verified native adapter; bind an explicit ACP or command entry")
		}
		if len(b.Command) == 0 {
			name := b.Host
			if name == "claude-code" {
				name = "claude"
			}
			b.Command = []string{name}
		}
	case "acp", "command":
		if len(b.Command) == 0 {
			return errors.New("ACP/command mode requires explicit command argv")
		}
		if b.Host == "" {
			b.Host = "custom"
		}
	default:
		return errors.New("mode must be native, acp, or command")
	}
	for _, v := range append(append([]string{}, b.Command...), b.Args...) {
		if strings.ContainsRune(v, 0) {
			return errors.New("NUL in command argv")
		}
	}
	if strings.ContainsRune(b.HostAgent, 0) || strings.HasPrefix(b.HostAgent, "-") {
		return errors.New("invalid host_agent")
	}
	for k, v := range b.Env {
		if k == "" || strings.ContainsAny(k, "=\x00") || strings.ContainsRune(v, 0) || strings.HasPrefix(strings.ToUpper(k), "EIGENFLUX_") {
			return errors.New("env must not override EigenFlux routing")
		}
	}
	bin, err := exec.LookPath(b.Command[0])
	if err != nil {
		return errors.New("agent executable not found; bind an absolute executable path")
	}
	bin, err = filepath.Abs(bin)
	if err != nil {
		return err
	}
	switch strings.ToLower(filepath.Ext(bin)) {
	case ".cmd", ".bat", ".ps1":
		return errors.New("bind a native executable or explicit node/python interpreter and script argv, not a shell shim")
	}
	b.Command[0] = bin
	if len(b.Events) == 0 {
		b.Events = []string{"pm_push"}
	}
	seen := map[string]bool{}
	for _, kind := range b.Events {
		switch kind {
		case "pm_push", "profile_review_due", "maintenance_due", "control_pending":
		default:
			return fmt.Errorf("unsupported event %q", kind)
		}
		if seen[kind] {
			return errors.New("duplicate event binding")
		}
		seen[kind] = true
	}
	return nil
}

func (b Binding) Handles(kind string) bool {
	for _, k := range b.Events {
		if k == kind {
			return true
		}
	}
	return false
}

func NewRevision() (string, error) {
	var v [16]byte
	_, err := rand.Read(v[:])
	return hex.EncodeToString(v[:]), err
}

// WriteJSON keeps the previous state on failure; callers own the account lock.
func WriteJSON(path string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".dispatch-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(raw)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return replaceFile(f.Name(), path)
}
