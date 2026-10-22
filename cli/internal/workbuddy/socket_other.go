//go:build !darwin && !linux && !windows

package workbuddy

import "errors"

func socketOwner(string) error { return errors.New("workbuddy_host_unsupported_platform") }
