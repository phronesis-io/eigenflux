package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"cli.eigenflux.ai/internal/config"
	"cli.eigenflux.ai/internal/filelock"
	"cli.eigenflux.ai/internal/maintenance"
	"github.com/spf13/cobra"
)

// This receipt is local only and contains hashes rather than account metadata
// or the recovery action. It does not assert that the host delivered a notice.
type watchRecoveryNotice struct {
	Version      int    `json:"version"`
	Scope        string `json:"scope"`
	Fingerprint  string `json:"fingerprint,omitempty"`
	Active       bool   `json:"active"`
	ID           string `json:"id,omitempty"`
	Acknowledged bool   `json:"acknowledged,omitempty"`
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
	if state.ID != "" && !validWatchRecoveryNoticeID(state.ID) {
		return watchRecoveryNotice{}, errors.New("invalid watch recovery notice ID")
	}
	// Version 1 receipts without an ID predate explicit acknowledgment.
	if state.ID == "" {
		state.Acknowledged = false
	}
	return state, nil
}

func validWatchRecoveryNoticeID(id string) bool {
	decoded, err := hex.DecodeString(id)
	return err == nil && len(decoded) == 16 && hex.EncodeToString(decoded) == id
}

func lockWatchRecoveryNotice(path string) (*filelock.Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, errors.New("state_lock_failed")
	}
	lock, acquired, err := filelock.TryAcquire(path + ".lock")
	if err != nil {
		return nil, errors.New("state_lock_failed")
	}
	if !acquired {
		return nil, errors.New("state_locked")
	}
	return lock, nil
}

func annotateWatchRecoveryNotice(cmd *cobra.Command, cfg *config.Config, r *updateWatchRecovery) {
	if r == nil || r.Action == "" && r.Status != "ready" {
		return
	}
	r.Notification, r.NotificationID = "", ""
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
	lock, err := lockWatchRecoveryNotice(path)
	if err != nil {
		diagnostic(err.Error())
		return
	}
	defer lock.Release()
	previous, readErr := readWatchRecoveryNotice(path, scope)
	if readErr != nil && !os.IsNotExist(readErr) {
		diagnostic("state_read_failed")
	}
	state := watchRecoveryNotice{Version: 1, Scope: scope}
	if r.Action != "" {
		digest := sha256.Sum256([]byte(r.Status + "\x00" + r.Reason + "\x00" + r.Action))
		state.Active, state.Fingerprint = true, hex.EncodeToString(digest[:])
	} else {
		if readErr != nil {
			return
		}
		r.Notification = "recovered"
	}
	// Recheck before suppressing or saving so a changed account cannot consume
	// another identity's notice while this command is finishing.
	currentPath, currentScope, err := watchRecoveryNoticeScope(cfg)
	if err != nil || currentPath != path || currentScope != scope {
		diagnostic("identity_changed")
		return
	}
	if readErr == nil && previous.Active == state.Active && previous.Fingerprint == state.Fingerprint && previous.ID != "" {
		state = previous
	} else {
		state.ID = maintenance.NewID()
		if err := saveMaintenance(path, state); err != nil {
			diagnostic("state_write_failed")
			return
		}
	}
	if state.Acknowledged {
		r.Notification = ""
		if state.Active {
			r.Notification = "unchanged"
		}
	} else {
		r.NotificationID = state.ID
	}
}

func acknowledgeWatchRecoveryNotice(id string) error {
	if !validWatchRecoveryNoticeID(id) {
		return errors.New("notice must be a 32-character lowercase hexadecimal ID")
	}
	path, scope, err := watchRecoveryNoticeScope(nil)
	if err != nil {
		return errors.New("recovery notice identity unavailable")
	}
	lock, err := lockWatchRecoveryNotice(path)
	if err != nil {
		return err
	}
	defer lock.Release()
	state, err := readWatchRecoveryNotice(path, scope)
	if err != nil || state.ID != id {
		return errors.New("recovery notice is unavailable or stale for this account")
	}
	currentPath, currentScope, err := watchRecoveryNoticeScope(nil)
	if err != nil || currentPath != path || currentScope != scope {
		return errors.New("recovery notice identity changed")
	}
	if state.Acknowledged {
		return nil
	}
	state.Acknowledged = true
	if err := saveMaintenance(path, state); err != nil {
		return errors.New("could not save recovery notice acknowledgment")
	}
	return nil
}

func newHeartbeatRecoveryNoticeAckCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use: "recovery-notice-ack", Short: "Acknowledge a submitted host recovery notice locally", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			id, _ := cmd.Flags().GetString("notice")
			if err := acknowledgeWatchRecoveryNotice(id); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Recovery notice acknowledgment recorded locally.")
			return nil
		},
	}
	cmd.Flags().String("notice", "", "Pending notice ID from this account's heartbeat plan")
	return cmd
}

func init() { heartbeatCmd.AddCommand(newHeartbeatRecoveryNoticeAckCmd()) }
