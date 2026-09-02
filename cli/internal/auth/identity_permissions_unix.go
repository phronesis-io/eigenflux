//go:build !windows

package auth

import (
	"fmt"
	"os"
)

func validatePrivateFilePermissions(info os.FileInfo) error {
	if info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("identity file permissions are too broad; require 0600")
	}
	return nil
}
