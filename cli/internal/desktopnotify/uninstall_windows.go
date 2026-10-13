package desktopnotify

import "context"

func platformUninstall(ctx context.Context) error {
	return platformRequest(ctx, "remove", Message{})
}
