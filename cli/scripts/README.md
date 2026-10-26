# Local installation

Keep this map updated when changing either installation entry.

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

## macOS paired test installation

`install-test-macos.sh` is the source-to-test upgrade entry for macOS. Require
an existing absolute Agent Home and explicit current host. It invokes devbuild
with a fresh in-memory test signing key and native architecture. The complete
CLI/helper/test-source/CDN package stays in a unique permanent directory under
`~/.local/share/eigenflux-test`; never copy only its binary.

Notification setup and status run before the CLI launcher or signed Skills are
replaced. A failed/denied setup leaves those unchanged and exits nonzero. The
human controls the system permission dialog. After readiness, publish an atomic
`~/.local/bin/eigenflux` launcher and sync the current account/host's paired
signed Skills. Sync failure exits nonzero and reports the partial installation.
The installer never provisions an identity, changes credentials, binds an Agent
or stops/restarts a watch. Verify those separately.

The launcher starts the package's loopback test source per invocation and
cleans up only that process and its unique temporary readiness directory.
It sets only `EIGENFLUX_CDN_URL`; account Home/server and Skills target remain
caller-selected. The executable and helper stay together for native setup.
Packages may be in use by existing bindings, so old packages are not deleted.
