package desktopnotify

import "context"

// Uninstall requires the caller to confirm that no other installation uses this
// per-user integration. It never changes the OS notification permission store.
func Uninstall(ctx context.Context) error { return platformUninstall(ctx) }
