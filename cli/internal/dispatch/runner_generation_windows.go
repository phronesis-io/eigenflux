//go:build windows

package dispatch

import "errors"

func GenerationProcessGone(pid int) (bool, error) {
	return false, errors.New("generation_process_group_unavailable")
}
