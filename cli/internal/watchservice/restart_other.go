//go:build !darwin && !windows

package watchservice

import (
	"context"
	"errors"
)

func platformRestart(context.Context, Target) (Result, error) {
	return Result{}, errors.New("watch restart supports macOS LaunchAgents and Windows scheduled tasks; restart this platform's existing supervisor directly")
}
