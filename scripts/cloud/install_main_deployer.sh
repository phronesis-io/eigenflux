#!/usr/bin/env bash
set -Eeuo pipefail

readonly INSTALL_PATH="/usr/local/sbin/eigenflux-deploy-main"
PROJECT_ROOT="$(cd "$(dirname "$0")/../.."; pwd)"
SOURCE_PATH="${PROJECT_ROOT}/scripts/cloud/deploy_main.sh"

die() {
  printf 'install_main_deployer: %s\n' "$*" >&2
  exit 1
}

file_owner() {
  stat -c %U "$1" 2>/dev/null || stat -f %Su "$1"
}

[[ "${EUID}" -eq 0 ]] || die "run this installer as root"
[[ "$#" -eq 0 ]] || die "this installer accepts no arguments"
[[ "$(file_owner "${PROJECT_ROOT}")" == "root" ]] || die "project root must be owned by root"
[[ "$(file_owner "${PROJECT_ROOT}/.git")" == "root" ]] || die ".git must be owned by root"
ownership_mismatch="$(find "${PROJECT_ROOT}" -xdev \( ! -user root -o ! -group root \) -print -quit)"
[[ -z "${ownership_mismatch}" ]] ||
  die "repository contains a path not managed by root:root: ${ownership_mismatch}"
[[ "$(git -c core.hooksPath=/dev/null -C "${PROJECT_ROOT}" symbolic-ref --quiet --short HEAD 2>/dev/null || true)" == "main" ]] ||
  die "installer must run from the production main worktree"
[[ -z "$(git -c core.hooksPath=/dev/null -C "${PROJECT_ROOT}" status --porcelain=v1 --untracked-files=all)" ]] ||
  die "production worktree is dirty"
[[ "$(git -c core.hooksPath=/dev/null -C "${PROJECT_ROOT}" rev-parse HEAD)" == \
   "$(git -c core.hooksPath=/dev/null -C "${PROJECT_ROOT}" rev-parse refs/remotes/origin/main)" ]] ||
  die "production HEAD must equal origin/main before installation"

bash -n "${SOURCE_PATH}"
install -o root -g root -m 0755 "${SOURCE_PATH}" "${INSTALL_PATH}.new"
mv -f "${INSTALL_PATH}.new" "${INSTALL_PATH}"

printf 'Installed %s\n' "${INSTALL_PATH}"
printf 'Run deployments with: sudo %s\n' "${INSTALL_PATH}"
