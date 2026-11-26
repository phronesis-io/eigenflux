#!/usr/bin/env bash
#
# install_lr_model.sh — atomically install or roll back an LR or GBDT ranking model
# bundle for the sort service.
#
# The sort service hot-reloads the model at:
#   $MODEL_ROOT/current/model.json
# where `current` is a symlink to a versioned bundle directory. This script
# stages a bundle, verifies its integrity, then flips the `current` symlink
# atomically so the running service never observes a partial bundle. The
# previous version is remembered via a `previous` symlink for one-command
# rollback.
#
# Layout:
#   $MODEL_ROOT/
#     versions/<model_version>/{model.json,feature_contract.json,checksums.sha256,...}
#     current  -> versions/<model_version>
#     previous -> versions/<previous_version>
#
# Delivery: the bundle is produced by eigenflux-ml and uploaded to
#   oss://<bucket>/rec/model/lr/sample_date=YYYY-MM-DD/<model_version>/
# Pass --oss <oss_uri> to pull it with `ossutil` (requires ossutil configured,
# e.g. via ECS RAM role), or --src <local_dir> to install an already-synced
# bundle. Only local files are ever read by the sort process itself.
#
# Usage:
#   install_lr_model.sh --src         /path/to/bundle_dir
#   install_lr_model.sh --oss         oss://eigenflux/rec/model/lr/sample_date=2026-08-03/lr_20260803_1625_e69f47d/
#   install_lr_model.sh --oss-latest  # resolve the newest sample_date + version and install it
#   install_lr_model.sh --oss-latest gbdt  # pull the latest discovery GBDT bundle
#   install_lr_model.sh --rollback
#   install_lr_model.sh --list
#
# Environment:
#   MODEL_ROOT        default /data/models/eigenflux/lr-ranker (discovery-gbdt for GBDT latest)
#   OSS_BUCKET        default eigenflux
#   OSS_MODEL_PREFIX  default rec/model/lr (rec/model/discovery_gbdt for GBDT latest)
#   OSS_ENDPOINT      default oss-ap-southeast-1-internal.aliyuncs.com (use the
#                     public endpoint from outside the aliyun VPC)
set -euo pipefail

latest_family=lr
if [[ "${1:-}" == --oss-latest ]]; then
  if [[ "${OSS_MODEL_PREFIX:-}" == rec/model/discovery_gbdt ]]; then latest_family=gbdt; fi
  latest_family="${2-$latest_family}"
  if [[ "$latest_family" == gbdt ]]; then
    OSS_MODEL_PREFIX="${OSS_MODEL_PREFIX:-rec/model/discovery_gbdt}"
    MODEL_ROOT="${MODEL_ROOT:-/data/models/eigenflux/discovery-gbdt}"
  fi
fi

MODEL_ROOT="${MODEL_ROOT:-/data/models/eigenflux/lr-ranker}"
VERSIONS_DIR="$MODEL_ROOT/versions"
CURRENT_LINK="$MODEL_ROOT/current"
PREVIOUS_LINK="$MODEL_ROOT/previous"

# OSS layout produced by eigenflux-ml (immutable, no server-side "latest"
# pointer). Override via env when the bucket/prefix/endpoint differ. On an
# aliyun ECS in the same region use the -internal endpoint with a RAM role;
# from outside the VPC use the public endpoint plus AK/SK in ossutil config.
OSS_BUCKET="${OSS_BUCKET:-eigenflux}"
OSS_MODEL_PREFIX="${OSS_MODEL_PREFIX:-rec/model/lr}"
OSS_ENDPOINT="${OSS_ENDPOINT:-oss-ap-southeast-1-internal.aliyuncs.com}"

log()  { printf '[install_lr_model] %s\n' "$*"; }
die()  { printf '[install_lr_model] ERROR: %s\n' "$*" >&2; exit 1; }

ossutil_cmd() {
  command -v ossutil >/dev/null 2>&1 || die "ossutil not found; install/configure it (endpoint=$OSS_ENDPOINT, RAM role or AK/SK) or use --src"
  if [ -n "${OSS_ENDPOINT:-}" ]; then
    ossutil -e "$OSS_ENDPOINT" "$@"
  else
    ossutil "$@"
  fi
}

# resolve_latest_oss prints the full oss:// URI of the newest model bundle by
# enumerating sample_date= partitions (newest date) then the model_version dir
# under it. There is no "latest" object in OSS, so this is the canonical way to
# find the most recent bundle.
resolve_latest_oss() {
  local base="oss://$OSS_BUCKET/$OSS_MODEL_PREFIX/"
  local listing latest_date
  listing="$(ossutil_cmd ls "$base" -d)" || die "failed to list $base"
  latest_date="$(printf '%s\n' "$listing" \
    | grep -oE 'sample_date=[0-9]{4}-[0-9]{2}-[0-9]{2}/?' | tr -d '/' | sort -u | tail -1)" || true
  [ -n "$latest_date" ] || die "no sample_date= partitions under $base"
  local date_uri="$base$latest_date/"
  local latest_version version_prefix="$1"
  listing="$(ossutil_cmd ls "$date_uri" -d)" || die "failed to list $date_uri"
  latest_version="$(printf '%s\n' "$listing" \
    | grep -oE "(^|/)${version_prefix}_[0-9]{8}_[0-9]{4}_[0-9a-f]+/?$" | tr -d '/' | sort -u | tail -1)" || true
  [ -n "$latest_version" ] || die "no ${version_prefix}_* model_version dir under $date_uri"
  printf '%s%s/' "$date_uri" "$latest_version"
}

verify_bundle() {
  # Verify checksums.sha256 (produced by eigenflux-ml) inside a staged dir.
  local dir="$1"
  [ -f "$dir/model.json" ] || die "bundle missing model.json: $dir"
  [ -f "$dir/checksums.sha256" ] || die "bundle missing checksums.sha256: $dir"
  ( cd "$dir" && sha256sum -c checksums.sha256 >/dev/null ) \
    || die "checksum verification failed for $dir"
  local version
  version="$(python3 -c "import json,sys;print(json.load(open(sys.argv[1]))['model_version'])" "$dir/model.json")" \
    || die "could not read model_version: $dir"
  [[ -n "$version" && "$version" != */* && "$version" != . && "$version" != .. ]] || die "invalid model_version"
  if [[ -n "${2:-}" && "$version" != "$2" ]]; then
    die "model_version $version does not match expected version $2"
  fi
  if [[ "$version" == gbdt_* ]]; then
    local artifact
    for artifact in model report; do
      grep -Eq "^[[:xdigit:]]{64} [ *]${artifact}[.]json$" "$dir/checksums.sha256" \
        || die "missing checksum for ${artifact}.json: $dir"
    done
  fi
  printf '%s\n' "$version"
}

atomic_relink() {
  # Atomically point $1 (link path) at $2 (target). os.replace renames a temp
  # symlink over the existing one in a single syscall (atomic on POSIX) and,
  # unlike `mv`, never dereferences a symlink-to-directory destination — so this
  # is portable across GNU and BSD/macOS.
  python3 - "$1" "$2" <<'PY'
import os, sys, tempfile
link, target = sys.argv[1], sys.argv[2]
d = os.path.dirname(os.path.abspath(link))
tmp = tempfile.mktemp(dir=d)
os.symlink(target, tmp)
os.replace(tmp, link)
PY
}

install_from_dir() {
  local staged="$1"
  local version; version="$(verify_bundle "$staged" "${2:-}")"

  mkdir -p "$VERSIONS_DIR"
  local dest="$VERSIONS_DIR/$version"
  if [ -d "$dest" ]; then
    log "version $version already installed; re-verifying"
    verify_bundle "$dest" "$version" >/dev/null
  else
    local tmp="$dest.tmp.$$"
    rm -rf "$tmp"
    cp -a "$staged" "$tmp"
    verify_bundle "$tmp" "$version" >/dev/null
    mv "$tmp" "$dest"
  fi

  # Remember the outgoing version as `previous` before flipping `current`.
  local target; target="$(readlink -f "$dest")"
  if [ -L "$CURRENT_LINK" ]; then
    local cur; cur="$(readlink -f "$CURRENT_LINK" || true)"
    if [ "$cur" = "$target" ]; then
      log "current already points to $version; rollback target unchanged"
      return
    fi
    [ -n "$cur" ] && atomic_relink "$PREVIOUS_LINK" "$cur"
  fi
  atomic_relink "$CURRENT_LINK" "$target"
  log "current -> $version (sort hot-reloads within its reload interval)"
}

rollback() {
  [ -L "$PREVIOUS_LINK" ] || die "no previous version to roll back to"
  local prev; prev="$(readlink -f "$PREVIOUS_LINK")"
  [ -d "$prev" ] || die "previous target missing: $prev"
  verify_bundle "$prev" "${prev##*/}" >/dev/null
  local cur; cur="$(readlink -f "$CURRENT_LINK" || true)"
  atomic_relink "$CURRENT_LINK" "$prev"
  [ -n "$cur" ] && atomic_relink "$PREVIOUS_LINK" "$cur"
  log "rolled back current -> $(basename "$prev")"
}

pull_oss() {
  local uri="$1" out="$2"
  mkdir -p "$out"
  log "pulling bundle from $uri"
  ossutil_cmd cp -r -f "$uri" "$out/" >/dev/null
}

install_from_oss() (
  local uri="$1"
  local staging; staging="$(mktemp -d)"
  trap 'rm -rf "$staging"' EXIT
  pull_oss "$uri" "$staging"
  # ossutil recreates the trailing path component; find the dir with model.json.
  local model; model="$(find "$staging" -type f -name model.json -print -quit)"
  [ -n "$model" ] || die "no model.json found under pulled bundle"
  install_from_dir "${model%/*}" "${2:-}"
)

main() {
  [ $# -ge 1 ] || die "usage: --src <dir> | --oss <uri> | --oss-latest [lr|gbdt] | --rollback | --list"
  case "$1" in
    --src)
      [ $# -eq 2 ] || die "--src requires a bundle directory"
      install_from_dir "$2"
      ;;
    --oss)
      [ $# -eq 2 ] || die "--oss requires an oss:// uri"
      install_from_oss "$2"
      ;;
    --oss-latest)
      [ $# -le 2 ] || die "--oss-latest accepts one optional family: lr or gbdt"
      local uri version
      case "$latest_family" in lr|gbdt) ;; *) die "unknown model family: $latest_family" ;; esac
      uri="$(resolve_latest_oss "$latest_family")"
      version="${uri%/}"
      log "latest bundle: $uri"
      install_from_oss "$uri" "${version##*/}"
      ;;
    --rollback)
      rollback
      ;;
    --list)
      log "root: $MODEL_ROOT"
      [ -L "$CURRENT_LINK" ]  && log "current  -> $(basename "$(readlink -f "$CURRENT_LINK")")"
      [ -L "$PREVIOUS_LINK" ] && log "previous -> $(basename "$(readlink -f "$PREVIOUS_LINK")")"
      [ -d "$VERSIONS_DIR" ]  && ls -1 "$VERSIONS_DIR"
      ;;
    *)
      die "unknown option: $1"
      ;;
  esac
}

main "$@"
