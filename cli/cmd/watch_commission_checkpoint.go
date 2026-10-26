package cmd

import (
	"context"
	"errors"
	"os"
	"time"

	"cli.eigenflux.ai/internal/dispatch"
)

// Retry only read failures, never a positive identity/configuration mismatch or
// an Agent invocation. Each subsequent mutation still performs its own guard.
func (w *accountWatch) commissionFulfillmentIdentityOK(ctx context.Context) error {
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := w.commissionIdentityOK()
		var local *os.PathError
		if err == nil || isWatchTerminal(err) || errors.Is(err, os.ErrPermission) || !errors.As(err, &local) || attempt == 2 {
			return err
		}
		if !watchPause(ctx, time.Duration(attempt+1)*100*time.Millisecond) {
			return ctx.Err()
		}
	}
}

func commissionLocalCheckCode(err error) string {
	switch {
	case errors.Is(err, errWatchIdentity):
		return "identity_changed"
	case errors.Is(err, errWatchConfiguration):
		return "configuration_changed"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "commission_local_check_cancelled"
	case errors.Is(err, os.ErrPermission):
		return "commission_local_permission_denied"
	case errors.Is(err, os.ErrNotExist):
		return "commission_local_state_missing"
	}
	var local *os.PathError
	if errors.As(err, &local) {
		return "commission_local_io_unavailable"
	}
	return "commission_local_check_unavailable"
}

func (w *accountWatch) failCommissionLocalCheck(job dispatch.Job, err error, session string) error {
	if saveErr := w.finishDispatch(job, "failed", commissionLocalCheckCode(err), session, ""); saveErr != nil {
		return saveErr
	}
	// Fixed categories retain a useful diagnosis without exposing local content.
	return w.diagnostic("commission_local_check", err)
}
