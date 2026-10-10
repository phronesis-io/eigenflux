//go:build !darwin && !windows

package desktopnotify

import "context"

func platformRequest(context.Context, string, Message) error { return ErrUnsupported }
