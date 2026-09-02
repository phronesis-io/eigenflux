package auth

import "os"

// Windows reports synthesized POSIX mode bits that do not describe the file's
// access-control list. The identity path still has to pass the regular-file and
// symlink checks in LoadOrCreateIdentity.
func validatePrivateFilePermissions(_ os.FileInfo) error {
	return nil
}
