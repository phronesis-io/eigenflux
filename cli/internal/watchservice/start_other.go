//go:build !windows

package watchservice

import (
	"context"
	"errors"
)

func platformStartNotifications(context.Context, NotificationTask) (Result, error) {
	return Result{}, errors.New("watch start currently supports Windows; use your existing host lifecycle on other platforms")
}
