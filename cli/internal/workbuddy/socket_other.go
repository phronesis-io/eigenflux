//go:build !darwin && !linux

package workbuddy

import "errors"

func socketOwner(string) error { return errors.New("workbuddy_host_unsupported_platform") }
