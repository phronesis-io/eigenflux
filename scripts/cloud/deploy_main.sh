#!/usr/bin/env bash
set -Eeuo pipefail

# Production entry point for EigenFlux backend deployments.
#
# Public invocation (no arguments):
#   sudo /usr/local/sbin/eigenflux-deploy-main
#
# The public invocation re-executes itself as a transient systemd service so
# every line is retained in journald. The internal argument is accepted only
# from a systemd service invocation.

readonly PROJECT_ROOT="/data/git/eigenflux"
readonly REMOTE_URL="https://github.com/phronesis-io/eigenflux.git"
readonly REMOTE_MAIN_REF="refs/remotes/origin/main"
readonly GITHUB_API_URL="https://api.github.com"
readonly GITHUB_REPOSITORY="phronesis-io/eigenflux"
readonly INSTALL_PATH="/usr/local/sbin/eigenflux-deploy-main"
readonly LOCK_PATH="/run/lock/eigenflux-deploy-main.lock"
readonly REQUIRED_REPO_OWNER="root"
readonly REQUIRED_REPO_GROUP="root"
readonly ALLOW_NON_ROOT_INTERNAL=0
readonly INTERNAL_ARGUMENT="--systemd-internal"
readonly BUILD_SERVICES=(profile item sort feed pm auth notification trade api ws pipeline cron replay)
DEPLOY_STAGE_DIR=""

log() {
  printf '[eigenflux-deploy] %s\n' "$*"
}

die() {
  log "ERROR: $*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

file_owner() {
  stat -c %U "$1" 2>/dev/null || stat -f %Su "$1"
}

git_safe() {
  git -c core.hooksPath=/dev/null "$@"
}

require_clean_worktree() {
  local phase="$1"
  local dirty

  dirty="$(git_safe -C "${PROJECT_ROOT}" status --porcelain=v1 --untracked-files=all)"
  if [[ -n "${dirty}" ]]; then
    log "worktree is dirty during ${phase}:" >&2
    printf '%s\n' "${dirty}" >&2
    die "refusing to deploy a dirty worktree"
  fi
}

require_managed_repository() {
  local mismatch

  [[ "$(file_owner "${PROJECT_ROOT}")" == "${REQUIRED_REPO_OWNER}" ]] ||
    die "project root must be owned by ${REQUIRED_REPO_OWNER}"
  [[ "$(file_owner "${PROJECT_ROOT}/.git")" == "${REQUIRED_REPO_OWNER}" ]] ||
    die ".git must be owned by ${REQUIRED_REPO_OWNER}"

  mismatch="$(find "${PROJECT_ROOT}" -xdev \
    \( ! -user "${REQUIRED_REPO_OWNER}" -o ! -group "${REQUIRED_REPO_GROUP}" \) \
    -print -quit)"
  [[ -z "${mismatch}" ]] ||
    die "repository contains a path not managed by ${REQUIRED_REPO_OWNER}:${REQUIRED_REPO_GROUP}: ${mismatch}"
}

verify_merged_pull_request() {
  local target="$1"
  local response_file="$2"
  local url

  url="${GITHUB_API_URL}/repos/${GITHUB_REPOSITORY}/commits/${target}/pulls"
  curl --fail --silent --show-error \
    --header 'Accept: application/vnd.github+json' \
    --header 'X-GitHub-Api-Version: 2022-11-28' \
    "${url}" >"${response_file}"

  jq --exit-status \
    --arg target "${target}" \
    'any(.[]; .merged_at != null and .base.ref == "main" and
      ((.merge_commit_sha // "") == $target or (.head.sha // "") == $target))' \
    "${response_file}" >/dev/null ||
    die "target ${target} is not associated with a merged pull request into main"
}

run_deployment() {
  local started_at run_id stage_dir pr_response target latest_target before after service

  export PATH='/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin'
  umask 022

  require_command git
  require_command curl
  require_command jq
  require_command tar
  require_command install
  require_command find
  require_command flock

  [[ "${EUID}" -eq 0 || "${ALLOW_NON_ROOT_INTERNAL}" -eq 1 ]] ||
    die "deployment body must run as root"
  [[ -d "${PROJECT_ROOT}/.git" ]] || die "not a Git worktree: ${PROJECT_ROOT}"
  require_managed_repository

  exec 9>"${LOCK_PATH}"
  flock --nonblock 9 || die "another EigenFlux deployment is already running"

  started_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  run_id="$(date -u +%Y%m%dT%H%M%SZ)-$$"
  stage_dir="$(mktemp -d "/var/tmp/eigenflux-deploy-${run_id}.XXXXXX")"
  DEPLOY_STAGE_DIR="${stage_dir}"
  pr_response="${stage_dir}/pulls.json"

  cleanup() {
    local exit_code=$?
    trap - EXIT
    [[ -z "${DEPLOY_STAGE_DIR}" ]] || rm -rf -- "${DEPLOY_STAGE_DIR}"
    if [[ "${exit_code}" -eq 0 ]]; then
      log "completed successfully"
    else
      log "failed with exit code ${exit_code}"
    fi
    exit "${exit_code}"
  }
  trap cleanup EXIT
  trap 'die "command failed at line ${LINENO}: ${BASH_COMMAND}"' ERR

  log "run_id=${run_id} operator=${SUDO_USER:-${USER:-unknown}} started_at=${started_at}"
  log "project_root=${PROJECT_ROOT}"

  require_clean_worktree "preflight"

  [[ "$(git_safe -C "${PROJECT_ROOT}" symbolic-ref --quiet --short HEAD 2>/dev/null || true)" == "main" ]] ||
    die "production worktree must be on branch main"

  before="$(git_safe -C "${PROJECT_ROOT}" rev-parse HEAD)"
  log "before_sha=${before}"
  log "fetching main from fixed HTTPS remote"
  git_safe -C "${PROJECT_ROOT}" fetch --no-tags "${REMOTE_URL}" \
    "+refs/heads/main:${REMOTE_MAIN_REF}"
  target="$(git_safe -C "${PROJECT_ROOT}" rev-parse "${REMOTE_MAIN_REF}^{commit}")"
  log "target_sha=${target}"

  git_safe -C "${PROJECT_ROOT}" merge-base --is-ancestor "${before}" "${target}" ||
    die "target is behind or divergent from the production HEAD; rollback and branch deployments are forbidden"

  verify_merged_pull_request "${target}" "${pr_response}"
  log "merged pull request verification passed"

  log "exporting target into isolated build directory"
  git_safe -C "${PROJECT_ROOT}" archive --format=tar "${target}" |
    tar -xf - -C "${stage_dir}"

  if ! cmp -s \
    "${stage_dir}/skills/ef-broadcast/references/contract.md" \
    "${stage_dir}/static/feed_contract.md"; then
    die "static/feed_contract.md is not synchronized in the target commit"
  fi

  log "building all backend services from target_sha=${target}"
  bash "${stage_dir}/scripts/common/build.sh" "${BUILD_SERVICES[@]}"
  for service in "${BUILD_SERVICES[@]}"; do
    [[ -x "${stage_dir}/build/${service}" ]] || die "missing build artifact: ${service}"
  done

  require_managed_repository
  require_clean_worktree "before fast-forward"
  log "confirming origin/main did not advance during the build"
  git_safe -C "${PROJECT_ROOT}" fetch --no-tags "${REMOTE_URL}" \
    "+refs/heads/main:${REMOTE_MAIN_REF}"
  latest_target="$(git_safe -C "${PROJECT_ROOT}" rev-parse "${REMOTE_MAIN_REF}^{commit}")"
  [[ "${latest_target}" == "${target}" ]] ||
    die "origin/main advanced during the build; rerun to deploy the new latest commit"

  log "fast-forwarding production main"
  git_safe -C "${PROJECT_ROOT}" merge --ff-only "${target}"
  [[ "$(git_safe -C "${PROJECT_ROOT}" rev-parse HEAD)" == "${target}" ]] ||
    die "production HEAD does not equal target after fast-forward"
  require_clean_worktree "after fast-forward"

  log "running database migrations"
  bash "${PROJECT_ROOT}/scripts/common/migrate_up.sh"
  require_clean_worktree "after migrations"

  log "installing verified build artifacts"
  mkdir -p "${PROJECT_ROOT}/build"
  for service in "${BUILD_SERVICES[@]}"; do
    install -m 0755 "${stage_dir}/build/${service}" "${PROJECT_ROOT}/build/${service}"
  done

  log "restarting all backend services"
  bash "${PROJECT_ROOT}/scripts/cloud/restart_all_services.sh"

  log "running service health checks"
  bash "${PROJECT_ROOT}/scripts/cloud/check_services.sh"

  after="$(git_safe -C "${PROJECT_ROOT}" rev-parse HEAD)"
  [[ "${after}" == "${target}" ]] || die "deployed SHA changed during deployment"
  require_clean_worktree "final verification"
  log "deployed_sha=${after}"
}

main() {
  local unit

  if [[ "${INVOCATION_ID:-}" != "" ]]; then
    [[ "$#" -eq 1 && "$1" == "${INTERNAL_ARGUMENT}" ]] ||
      die "invalid internal invocation"
    run_deployment
    return
  fi

  [[ "$#" -eq 0 ]] || die "this command accepts no arguments"
  [[ "${EUID}" -eq 0 ]] || die "run with sudo: sudo ${INSTALL_PATH}"
  [[ "$(readlink -f "$0")" == "${INSTALL_PATH}" ]] ||
    die "run the root-managed installed copy: sudo ${INSTALL_PATH}"
  require_command systemd-run

  unit="eigenflux-deploy-$(date -u +%Y%m%dT%H%M%SZ)-$$"
  exec systemd-run \
    --unit="${unit}" \
    --description="EigenFlux origin/main deployment" \
    --collect \
    --wait \
    --pipe \
    --service-type=exec \
    "${INSTALL_PATH}" "${INTERNAL_ARGUMENT}"
}

main "$@"
