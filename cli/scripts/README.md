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

`devbuild -test-bundle` also serves a signed single-platform complete update ZIP
and both release manifests from its loopback CDN. The update ZIP contains only
the CLI and its paired macOS helper; it excludes the test server and wrappers.
The signing key stays in memory. Each build creates a different trust root,
so this source verifies its own same-version bootstrap, not updates from later
Git commits or independently rebuilt bundles. Rebuild/reinstall to follow the
test branch; no test artifact is published to the production CDN.

## Signed complete releases

`build.sh` creates six deterministic platform ZIPs alongside the six bare CLI
binaries. macOS ZIPs include the matching CPU's notification app. Other ZIPs
contain only the CLI; Windows notification resources are embedded. On Linux,
provide `EIGENFLUX_NOTIFIER_HELPERS_DIR` with both `darwin-{arch}` helper bundles.
macOS builds them locally when that variable is absent. Local ad-hoc helpers
are test-only and are rejected by production publication.

Production `Release CLI` runs on macOS. Configure repository secrets
`MACOS_CERTIFICATE_P12_BASE64`, `MACOS_CERTIFICATE_PASSWORD`,
`MACOS_SIGNING_IDENTITY`, `APPLE_ID`, `APPLE_TEAM_ID`, and
`APPLE_APP_SPECIFIC_PASSWORD`. The workflow uses a temporary keychain, signs
with Developer ID, notarizes/staples both helper apps, then signs the final ZIP
hashes in `package-release.json`. Missing credentials fail before publication. Local
release operators may provide `EIGENFLUX_NOTIFIER_SIGN_IDENTITY` and an existing
`EIGENFLUX_NOTARY_PROFILE` instead. Never clear quarantine or weaken Gatekeeper.

`publish.sh` verifies complete package bytes and Apple's signature, stapled
ticket and Gatekeeper acceptance on extracted macOS apps before any object-store
write. Publish immutable versioned ZIPs/binaries/checksums before changing the
signed latest pointer. Keep bare artifacts for existing clients; new clients
require signed complete packages and retain old version directories while in use.
Runtime business failures never replay work through an older release.
