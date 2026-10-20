package dispatch

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

// Connectivity is separate from implementation acceptance and business results.
type MainNoticeProbe struct {
	Protocol             string `json:"protocol"`
	Code                 string `json:"code"`
	TransportAvailable   bool   `json:"transport_available"`
	TargetMatched        bool   `json:"target_matched"`
	TargetStatus         string `json:"target_status,omitempty"`
	Enabled              bool   `json:"enabled"`
	ImplementationStatus string `json:"implementation_status"`
	HostVerified         bool   `json:"host_verified"`
}

// ProbeMainNotice sends only initialize and exact owner discovery. It does not
// start a daemon, read conversation contents or start/queue a model turn.
func ProbeMainNotice(parent context.Context, target MainNoticeTarget) MainNoticeProbe {
	capability := MainNoticeCapability(target.Host)
	probe := MainNoticeProbe{Protocol: capability.Protocol, ImplementationStatus: capability.Status, HostVerified: MainNoticeHostVerified(target.Host), Code: "main_notice_host_unverified"}
	if target.Host != "codex" {
		return probe
	}
	if target.Binding.Host != target.Host || target.Binding.Mode != "native" || target.Binding.MainSessionID != target.SessionID || strings.TrimSpace(target.SessionID) == "" || len(target.SessionID) > 4096 || strings.HasPrefix(target.SessionID, "-") || strings.ContainsRune(target.SessionID, 0) || !utf8.ValidString(target.SessionID) {
		probe.Code = "main_notice_target_invalid"
		return probe
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	probe.Code = "main_notice_owner_ipc_unavailable"
	ipc, err := openCodexOwnerIPC(ctx, target.Binding)
	if err != nil {
		if errors.Is(err, errCodexOwnerIPCUnsupported) {
			probe.Code = "main_notice_owner_ipc_unsupported"
		}
		return probe
	}
	defer ipc.Close()
	probe.TransportAvailable = true
	probe.Code = "main_notice_owner_unavailable"
	if _, err := mainNoticeOwner(ctx, ipc, target.SessionID); err != nil {
		if errors.Is(err, ErrMainNoticeUnsupported) {
			probe.Code = "main_notice_owner_capability_unverified"
		}
		return probe
	}
	probe.TargetMatched = true
	probe.TargetStatus = "owner_found"
	probe.Enabled = MainNoticeBindingEnabled(target.Binding)
	probe.Code = "main_notice_owner_ready"
	if !probe.Enabled {
		probe.Code = "main_notice_hidden_input_unverified"
	}
	return probe
}
