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
be enabled in System Settings. Existing watch processes need a restart with
their original Home, server and arguments; installation does not change bindings
or terminate them. Linux does not install a desktop notification helper.

Run `go test ./tests -run TestLocalInstallNotificationSetup` from `cli/`. Fixtures
use a temporary Home and stub executables; they never request system permission.
