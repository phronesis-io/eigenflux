# Build the CLI on Windows

Use PowerShell 5.1 or newer, Git, and Go 1.25 or newer. Run from the repository
root. No Bash, GNU tar, OpenSSL, production signing key, or administrator session
is needed for a local test bundle. Respect the machine's PowerShell execution
policy; do not disable organization policy to run these scripts.

## Local branch testing

```powershell
.\cli\scripts\build.ps1 -TestBundle
```

The command prints a new directory under `build/` and its adjacent ZIP. It reads
the version from `cli/.cli.config` and embeds the actual Git commit. A dirty
checkout is marked `-dirty`. Use `-Arch arm64` or `-Arch amd64` to select a target;
the default follows the Go host architecture. Existing output directories are
never removed. `-OutDir` is relative to the repository root.

Inside the printed output directory, run:

```powershell
.\Run-Test.ps1 version
.\Run-Test.ps1 skills sync
.\Run-Test.ps1 heartbeat plan
```

`Run-Test.ps1` invokes the exact bundled EXE, serves its matching signed Skills
on a temporary loopback port, and stops that server after the command. It uses
`.test-state/.eigenflux` and `.test-state/skills` inside that bundle, restoring
the calling process environment afterward. It never changes PATH or the installed
CLI. The ZIP is created before any account state exists and contains no private
signing key. Copy that original ZIP to another Windows machine of the same
architecture; unzip it in a writable user directory. Do not repackage
`.test-state`, which may contain account credentials after testing.

The first `skills sync` verifies the real embedded key and signed bundle. A new
test Home has no configured account: use ordinary `server` and Agent setup
commands through the same runner before expecting a complete heartbeat plan.
Account setup and business API selection remain explicit; the local CDN only
serves build artifacts. Authentication or business-server failures after Skills
verification are separate from a missing verifier.

This is a test-only trust root generated in memory for each build. It will not
trust production CDN Skills. Use the runner for these tests; running the EXE
directly against the production CDN will report an untrusted signing key. A new
bundle uses fresh test state. It does not install a host plugin or prove that a
desktop Agent can be automatically invoked; those remain separate acceptance
tests. The runner is a manual developer tool, not a scheduler prompt. Native
scheduled tasks continue to use direct CLI commands under the host contract.

## Build an EXE for an existing trusted release source

Obtain the release source's **public** Ed25519 key from its trusted release
configuration. Do not take a trust root from an arbitrary downloaded manifest.

```powershell
$env:EIGENFLUX_SKILLS_VERIFY_PUBLIC_KEY = '<trusted base64 public key>'
.\cli\scripts\build.ps1
```

This mode requires no private key and builds an EXE plus `build-info.json` and
a ZIP. It does not publish or sign new Skills; the configured CDN must already
serve compatible signed rules. Missing or invalid keys fail before compilation.
Clear the public-key environment variable before choosing `-TestBundle`.

Do not use a bare `go build` for a runnable heartbeat release: it omits the
compiled verifier unless the required `-ldflags` are supplied. Setting the public
key only at runtime cannot repair an already-built executable. Production
releases still use the reviewed, main-only release workflow.
