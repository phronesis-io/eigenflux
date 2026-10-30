package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"cli.eigenflux.ai/internal/dispatch"
)

// Only accounts previously owned by the notification-only receiver need an
// initial authoritative order reconciliation. Keep it off process startup so
// a temporary Commission failure cannot stop PM/runtime/notification reception.
func (w *accountWatch) reconcileWorkBuddyMigration(ctx context.Context) (bool, error) {
	if w.binding == nil || w.binding.Host != "workbuddy" || w.binding.Mode != "acp" || !w.binding.Handles("commission_order") {
		return true, nil
	}
	owner, err := readNotificationWatch(w.home, w.server.Name)
	if os.IsNotExist(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if !sameBindingIdentity(owner, *w.binding) {
		// A prior account's historical notification marker cannot authorize
		// reconciliation or stop the current explicitly bound owner.
		return true, nil
	}
	marker := filepath.Join(w.home, "watch", w.scope+".workbuddy-migration.json")
	var completed struct {
		Revision string `json:"binding_revision"`
	}
	data, err := os.ReadFile(marker)
	if err == nil && json.Unmarshal(data, &completed) == nil && completed.Revision == w.binding.Revision {
		return true, nil
	}
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	if err = w.recoverUnrecordedWorkBuddyOrders(ctx); err != nil {
		return false, err
	}
	if err = w.commissionIdentityOK(); err != nil {
		return false, err
	}
	completed.Revision = w.binding.Revision
	return true, dispatch.WriteJSON(marker, completed)
}

func (w *accountWatch) workBuddyMigrationLoop(ctx context.Context) error {
	for ctx.Err() == nil {
		if !w.handoff.begin() {
			return nil
		}
		call, cancel := context.WithTimeout(ctx, 30*time.Second)
		done, err := w.reconcileWorkBuddyMigration(call)
		cancel()
		w.handoff.end()
		if err == nil && done {
			return nil
		}
		if isWatchTerminal(err) {
			return w.diagnostic("commission_migration", err)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		code := "order_reconciliation_pending"
		if errors.Is(err, context.DeadlineExceeded) {
			code = "order_reconciliation_timeout"
		}
		if emitErr := w.emit("commission_migration_status", map[string]string{"status": "pending", "code": code}); emitErr != nil {
			return emitErr
		}
		if !watchPause(ctx, time.Minute) {
			break
		}
	}
	return ctx.Err()
}
