package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"cli.eigenflux.ai/internal/config"
	"github.com/spf13/cobra"
)

// This receipt is local only and contains hashes rather than account metadata
// or the recovery action. It does not assert that the host delivered a notice.
type watchRecoveryNotice struct {
	Version     int    `json:"version"`
	Scope       string `json:"scope"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Active      bool   `json:"active"`
}

func watchRecoveryNoticeScope(cfg *config.Config) (path, scope string, err error) {
	if cfg == nil {
		cfg, err = config.Load()
		if err != nil {
			return "", "", err
		}
	}
	server, err := cfg.GetActive(serverFlag)
	if err != nil {
		return "", "", err
	}
	identity, err := watchBindingIdentity()
	if err != nil {
		return "", "", err
	}
	if identity.Server != server.Name || identity.Endpoint != server.Endpoint {
		return "", "", errWatchIdentity
	}
	sum := sha256.Sum256([]byte(identity.Home + "\x00" + identity.Server + "\x00" + identity.Endpoint + "\x00" + identity.Scope))
	scope = hex.EncodeToString(sum[:])
	return maintenancePathForServer("watch-recovery-notice-"+scope, identity.Server), scope, nil
}

func readWatchRecoveryNotice(path, scope string) (watchRecoveryNotice, error) {
	var state watchRecoveryNotice
	info, err := os.Lstat(path)
	if err != nil {
		return state, err
	}
	if !info.Mode().IsRegular() || info.Size() > 4096 {
		return state, errors.New("invalid watch recovery notice file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return state, err
	}
	if len(raw) > 4096 || json.Unmarshal(raw, &state) != nil || state.Version != 1 || state.Scope != scope {
		return watchRecoveryNotice{}, errors.New("invalid watch recovery notice")
	}
	if state.Active {
		digest, err := hex.DecodeString(state.Fingerprint)
		if err != nil || len(digest) != sha256.Size {
			return watchRecoveryNotice{}, errors.New("invalid watch recovery fingerprint")
		}
	}
	return state, nil
}

func annotateWatchRecoveryNotice(cmd *cobra.Command, cfg *config.Config, r *updateWatchRecovery) {
	if r == nil || r.Action == "" && r.Status != "ready" {
		return
	}
	r.Notification = ""
	if r.Action != "" {
		r.Notification = "required"
	}
	diagnostic := func(code string) {
		fmt.Fprintf(cmd.ErrOrStderr(), "[eigenflux:watch-recovery-notice] %s\n", code)
	}
	path, scope, err := watchRecoveryNoticeScope(cfg)
	if err != nil {
		diagnostic("identity_unavailable")
		return
	}
	previous, readErr := readWatchRecoveryNotice(path, scope)
	if readErr != nil && !os.IsNotExist(readErr) {
		diagnostic("state_read_failed")
	}
	state := watchRecoveryNotice{Version: 1, Scope: scope}
	if r.Action != "" {
		digest := sha256.Sum256([]byte(r.Status + "\x00" + r.Reason + "\x00" + r.Action))
		state.Active, state.Fingerprint = true, hex.EncodeToString(digest[:])
	} else if readErr != nil || !previous.Active {
		return
	}
	// Recheck before suppressing or saving so a changed account cannot consume
	// another identity's notice while this command is finishing.
	currentPath, currentScope, err := watchRecoveryNoticeScope(cfg)
	if err != nil || currentPath != path || currentScope != scope {
		diagnostic("identity_changed")
		return
	}
	if state.Active && readErr == nil && previous.Active && previous.Fingerprint == state.Fingerprint {
		r.Notification = "unchanged"
		return
	}
	if !state.Active {
		r.Notification = "recovered"
	}
	if err := saveMaintenance(path, state); err != nil {
		diagnostic("state_write_failed")
	}
}
