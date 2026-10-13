# Local installation

Keep this map updated when changing `install-local.sh`.

The installer builds the CLI and, on macOS, its notification application before
replacing installed files. The helper uses the CLI's Go target architecture and
requires Xcode Command Line Tools. Stage and replace the complete application;
do not merge bundles or follow a helper symlink. Preserve the previous helper
until the CLI replacement succeeds.

After installing Skills, macOS runs `notify setup` to request permission. A
failure retains its diagnostic and recovery commands, returns nonzero, and
leaves the installed CLI and Skills available. Previously denied permission must
be enabled in System Settings. Linux does not install a desktop notification helper.

Pass `--watch-launch-agent /absolute/path.plist` to restart one existing macOS
watch after installation. The target must be a readable regular plist directly
under the current user's `~/Library/LaunchAgents`; invalid arguments fail before
building. The new CLI's `watch restart --launch-agent` reloads the selected
service and preserves its wrapper, account and arguments. No service is inferred
or created. Without the flag, installation only reminds the caller to restart.
Notification setup failure still permits the requested restart; either failure
returns nonzero without printing `Done`. A started process does not prove network
readiness: check a fresh `runtime_ready` event and verify an actual message.

Run `go test ./tests -run TestLocalInstallNotificationSetup` from `cli/`. Fixtures
use a temporary Home and stub executables; they never request system permission.
