//go:build !windows

package dispatch

import "os"

func replaceFile(from, to string) error { return os.Rename(from, to) }
