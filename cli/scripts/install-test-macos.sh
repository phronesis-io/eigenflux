#!/bin/bash
# Build and install a paired macOS test CLI, notification helper and signed Skills.
set -euo pipefail

AGENT_HOME=""
SERVER=""
HOST=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --homedir|--server|--host)
      [[ $# -ge 2 && -n "$2" && "$2" != --* ]] || { echo "Missing value for $1" >&2; exit 2; }
      case "$1" in
        --homedir) AGENT_HOME="$2" ;;
        --server) SERVER="$2" ;;
        --host) HOST="$2" ;;
      esac
      shift 2 ;;
    *) echo "Usage: $0 --homedir <existing-Agent-Home> --host <current-host> [--server <server>]" >&2; exit 2 ;;
  esac
done
[[ "$(uname -s)" == Darwin ]] || { echo "This installer supports macOS only." >&2; exit 2; }
[[ "$AGENT_HOME" == /* && -d "$AGENT_HOME" && -n "$HOST" ]] || { echo "Specify the existing absolute Agent Home and current --host; no identity is created." >&2; exit 2; }
case "$HOST" in workbuddy|codex|claude-code|openclaw|hermes|terminal) ;; *) echo "Unknown host: $HOST" >&2; exit 2 ;; esac
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
CLI_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
command -v go >/dev/null || { echo "Go is required to build the test package." >&2; exit 1; }
ARCH="$(go env GOARCH)"
case "$ARCH" in arm64|amd64) ;; *) echo "Unsupported architecture: $ARCH" >&2; exit 1 ;; esac
PACKAGES="$HOME/.local/share/eigenflux-test"
mkdir -p "$PACKAGES"
# Each build has its own trust root; never overwrite a package used by a watch.
PACKAGE="$(mktemp -d "$PACKAGES/macos-test.XXXXXX")"
rmdir "$PACKAGE"
cd "$CLI_DIR"
EIGENFLUX_SKILLS_VERIFY_PUBLIC_KEY= go run ./cmd/devbuild -test-bundle -target "darwin/$ARCH" -out "$PACKAGE"
[[ -x "$PACKAGE/eigenflux" && -x "$PACKAGE/test-source" && -x "$PACKAGE/EigenFlux Notifications.app/Contents/MacOS/EigenFluxNotifications" ]] || { echo "Incomplete macOS test bundle; CLI launcher was not changed." >&2; exit 1; }

# The launcher keeps the complete package together and serves only its paired
# signed CDN for this invocation. Home/server remain the caller's CLI arguments
# or environment, so a shared executable does not merge Agent identities.
{
  printf '#!/bin/bash\nset -euo pipefail\nBUNDLE='
  printf '%q\n' "$PACKAGE"
  cat <<'LAUNCHER'
STATE="$(mktemp -d "${TMPDIR:-/tmp}/eigenflux-test.XXXXXX")"
SOURCE_PID=""
cleanup() {
  if [[ -n "$SOURCE_PID" ]]; then kill "$SOURCE_PID" 2>/dev/null || true; wait "$SOURCE_PID" 2>/dev/null || true; fi
  rm -f "$STATE/ready" "$STATE/source.log"
  rmdir "$STATE" 2>/dev/null || true
}
trap cleanup EXIT
trap 'exit 130' HUP INT TERM
"$BUNDLE/test-source" -serve "$BUNDLE/cdn" -ready-file "$STATE/ready" >"$STATE/source.log" 2>&1 &
SOURCE_PID=$!
for ((i=0; i<100; i++)); do
  [[ ! -s "$STATE/ready" ]] || break
  kill -0 "$SOURCE_PID" 2>/dev/null || { echo "Paired test source failed to start." >&2; exit 1; }
  sleep 0.1
done
[[ -s "$STATE/ready" ]] || { echo "Paired test source startup timed out." >&2; exit 1; }
export EIGENFLUX_CDN_URL="$(cat "$STATE/ready")"
"$BUNDLE/eigenflux" "$@"
LAUNCHER
} > "$PACKAGE/Run-Test.sh"
chmod 755 "$PACKAGE/Run-Test.sh"
ARGS=(--homedir "$AGENT_HOME")
[[ -z "$SERVER" ]] || ARGS+=(--server "$SERVER")

# Set up the helper before changing the caller's executable or signed Skills.
# macOS permission is still confirmed by the human in its own system dialog.
echo "Installing the macOS notification helper; allow EigenFlux if macOS asks."
if ! "$PACKAGE/Run-Test.sh" "${ARGS[@]}" notify setup || ! "$PACKAGE/Run-Test.sh" "${ARGS[@]}" notify status; then
  echo "Notification setup is incomplete; the CLI launcher and Skills were not replaced." >&2
  echo "If denied, enable EigenFlux in System Settings > Notifications, then retry this installer." >&2
  exit 1
fi
INSTALL_DIR="$HOME/.local/bin"
mkdir -p "$INSTALL_DIR"
[[ ! -d "$INSTALL_DIR/eigenflux" ]] || { echo "Refusing to replace an unexpected CLI directory." >&2; exit 1; }
STAGE="$(mktemp "$INSTALL_DIR/.eigenflux-test.XXXXXX")"
cp "$PACKAGE/Run-Test.sh" "$STAGE"
chmod 755 "$STAGE"
mv -f "$STAGE" "$INSTALL_DIR/eigenflux"
SYNC_LOG="$PACKAGE/install-skills.json"
if ! "$INSTALL_DIR/eigenflux" "${ARGS[@]}" skills sync --host "$HOST" --format json > "$SYNC_LOG"; then
  cat "$SYNC_LOG" >&2
  echo "Test CLI and notifications are installed, but signed Skills sync failed; installation remains incomplete." >&2
  exit 1
fi
cat "$SYNC_LOG"
if ! grep -Eq '"verified_manifest"[[:space:]]*:[[:space:]]*true' "$SYNC_LOG" || ! grep -Eq '"stale"[[:space:]]*:[[:space:]]*false' "$SYNC_LOG"; then
  echo "Signed test Skills were not verified; installation remains incomplete. Preserve local edits and inspect the sync diagnostic above." >&2
  exit 1
fi
"$INSTALL_DIR/eigenflux" "${ARGS[@]}" version
echo "Complete test package installed: $PACKAGE"
echo "CLI launcher: $INSTALL_DIR/eigenflux"
echo "Notifications are configured. Agent binding/watch readiness must be verified separately."
case ":$PATH:" in *":$INSTALL_DIR:"*) ;; *) echo "Add $INSTALL_DIR to PATH or use that absolute launcher path." ;; esac
