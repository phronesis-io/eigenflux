package dispatch

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

// MainNoticeProbe is connectivity evidence only. It cannot enable delivery or
// establish that a host hides additionalContext or admits an idle-only turn.
type MainNoticeProbe struct {
	Protocol           string `json:"protocol"`
	Code               string `json:"code"`
	TransportAvailable bool   `json:"transport_available"`
	TargetMatched      bool   `json:"target_matched"`
	TargetStatus       string `json:"target_status,omitempty"`
	Enabled            bool   `json:"enabled"`
}

// ProbeMainNotice uses only initialize and thread/read against the existing
// Codex host. Never resume, create, subscribe, enqueue or start a turn here.
func ProbeMainNotice(parent context.Context, target MainNoticeTarget) MainNoticeProbe {
	probe := MainNoticeProbe{Protocol: MainNoticeCapability(target.Host).Protocol, Code: "main_notice_host_unverified"}
	if target.Host != "codex" {
		return probe
	}
	if target.Binding.Host != target.Host || target.Binding.Mode != "native" || target.Binding.MainSessionID != target.SessionID || strings.TrimSpace(target.SessionID) == "" || len(target.SessionID) > 4096 || strings.HasPrefix(target.SessionID, "-") || strings.ContainsRune(target.SessionID, 0) || !utf8.ValidString(target.SessionID) {
		probe.Code = "main_notice_target_invalid"
		return probe
	}
	probe.Code = "main_notice_proxy_unavailable"
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	if ctx.Err() != nil {
		return probe
	}
	cmd, err := (Runner{Binding: target.Binding}).command([]string{"app-server", "proxy"})
	if err != nil {
		return probe
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return probe
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return probe
	}
	cmd.Stderr = io.Discard
	guard, err := startManaged(cmd)
	if err != nil {
		stdin.Close()
		stdout.Close()
		return probe
	}
	p := &mainNoticeRPC{ctx: ctx, stdin: stdin, incoming: make(chan acpRead, 16), stop: make(chan struct{})}
	defer func() {
		close(p.stop)
		stdin.Close()
		guard.kill()
		stdout.Close()
		_ = cmd.Wait()
		guard.close()
	}()
	go p.read(stdout)
	if _, err := p.call("initialize", map[string]any{"clientInfo": map[string]string{"name": "eigenflux_main_notice_probe", "version": "1"}, "capabilities": map[string]bool{"experimentalApi": true}}); err != nil {
		return probe
	}
	if p.write(map[string]any{"method": "initialized", "params": map[string]any{}}) != nil {
		return probe
	}
	probe.TransportAvailable = true
	probe.Code = "main_notice_target_unavailable"
	raw, err := p.call("thread/read", map[string]any{"threadId": target.SessionID, "includeTurns": false})
	if err != nil {
		return probe
	}
	var current struct {
		Thread struct {
			ID     string `json:"id"`
			Status struct {
				Type string `json:"type"`
			} `json:"status"`
		} `json:"thread"`
	}
	if json.Unmarshal(raw, &current) != nil || current.Thread.ID != target.SessionID {
		probe.Code = "main_notice_target_mismatch"
		return probe
	}
	probe.TargetMatched = true
	switch current.Thread.Status.Type {
	case "idle":
		probe.TargetStatus = "idle"
		probe.Code = "main_notice_hidden_input_unverified"
	case "active":
		probe.TargetStatus = "active"
		probe.Code = "main_notice_target_busy"
	case "notLoaded", "systemError":
		probe.TargetStatus = current.Thread.Status.Type
	default:
		probe.TargetStatus = "unknown"
	}
	return probe
}
