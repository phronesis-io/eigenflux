package cmd

import (
	"cli.eigenflux.ai/internal/desktopnotify"
	"context"
	"errors"
	"testing"
)

type upgradeNotifierFixture struct {
	checkErr, setupErr, prepareErr error
	checks, setups, repairs        int
}

func (n *upgradeNotifierFixture) Check(context.Context) error   { n.checks++; return n.checkErr }
func (n *upgradeNotifierFixture) Setup(context.Context) error   { n.setups++; return n.setupErr }
func (n *upgradeNotifierFixture) Prepare(context.Context) error { n.repairs++; return n.prepareErr }

func TestUpgradeNotificationsIndependentOfWatch(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		manual               bool
		check, setup, repair error
		status               string
		setups, repairs      int
	}{
		{name: "foreground", manual: true, status: "ready", setups: 1},
		{name: "foreground_denied", manual: true, setup: desktopnotify.ErrPermission, status: "permission_or_setup_required", setups: 1},
		{name: "foreground_permission_prompt_timeout", manual: true, setup: context.DeadlineExceeded, status: "permission_or_setup_required", setups: 1},
		{name: "background_ready", status: "ready"},
		{name: "background_repairs_without_prompt", check: desktopnotify.ErrHelperMissing, status: "ready", repairs: 1},
		{name: "background_repair_requires_permission", check: desktopnotify.ErrHelperMissing, repair: desktopnotify.ErrPermissionRequired, status: "permission_or_setup_required", repairs: 1},
		{name: "background_no_permission", check: desktopnotify.ErrPermissionRequired, status: "permission_or_setup_required"},
		{name: "background_denied", check: desktopnotify.ErrPermission, status: "permission_or_setup_required"},
		{name: "windows_registered", check: desktopnotify.ErrPendingFirstNotification, status: "registered"},
		{name: "provider_error", check: errors.New("failure"), status: "permission_or_setup_required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := &upgradeNotifierFixture{checkErr: tc.check, setupErr: tc.setup, prepareErr: tc.repair}
			r := checkUpgradeNotifications(context.Background(), tc.manual, n)
			if r.Status != tc.status || n.setups != tc.setups || n.repairs != tc.repairs || r.Visibility != "unknown" {
				t.Fatalf("result=%+v fixture=%+v", r, n)
			}
			if !tc.manual && n.setups != 0 {
				t.Fatal("background requested permission")
			}
		})
	}
}
