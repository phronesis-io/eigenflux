package cmd

import (
	"context"
	"errors"
	"os"
	"time"

	"cli.eigenflux.ai/internal/desktopnotify"
	"github.com/spf13/cobra"
)

type upgradeNotificationStatus struct {
	Status     string `json:"status"`
	Code       string `json:"code,omitempty"`
	Action     string `json:"action,omitempty"`
	Visibility string `json:"visibility"`
}

type upgradeNotifier interface {
	Check(context.Context) error
	Setup(context.Context) error
	Prepare(context.Context) error
}

// Explicit foreground upgrades may request permission. Automatic maintenance
// repairs installation only; macOS permission requests remain a user action.
func checkUpgradeNotifications(ctx context.Context, manual bool, n upgradeNotifier) upgradeNotificationStatus {
	var err error
	if manual {
		err = n.Setup(ctx)
	} else {
		err = n.Check(ctx)
		if errors.Is(err, desktopnotify.ErrHelperMissing) {
			err = n.Prepare(ctx)
		}
	}
	r := upgradeNotificationStatus{Status: "ready", Visibility: "unknown"}
	if err == nil {
		return r
	}
	d := desktopSetupStatus(err)
	r.Status = d["status"]
	r.Code = d["code"]
	r.Action = d["action"]
	if errors.Is(err, desktopnotify.ErrPermissionRequired) {
		r.Action = "run_notify_setup"
	}
	return r
}

func upgradeNotifications(cmd *cobra.Command, manual bool) upgradeNotificationStatus {
	if os.Getenv("EIGENFLUX_SKIP_AGENT_SETUP") == "1" {
		return upgradeNotificationStatus{Status: "skipped", Code: "owner_setup_disabled", Visibility: "unknown"}
	}
	parent := cmd.Context()
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 35*time.Second)
	defer cancel()
	source := "watch"
	if manual {
		source = "manual"
	}
	return checkUpgradeNotifications(ctx, manual, observedNotificationCLIFrom(source))
}
