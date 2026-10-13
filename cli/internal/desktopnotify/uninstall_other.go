//go:build !darwin && !windows

package desktopnotify

import "context"

func platformUninstall(context.Context) error { return nil }
