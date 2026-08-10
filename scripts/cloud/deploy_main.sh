#!/bin/bash
set -euo pipefail

# This source template is rendered by install_main_deployer.sh. The installed
# copy is root-owned and points to one fixed production checkout.
PROJECT_ROOT='@@PROJECT_ROOT@@'
DEPLOY_LIB='@@DEPLOY_LIB@@'
DEPLOY_USER='@@DEPLOY_USER@@'
DEPLOY_HOME='@@DEPLOY_HOME@@'
# The repository is public, so a fixed HTTPS URL avoids depending on mutable
# per-user SSH identity configuration while Git config remains fully disabled.
OFFICIAL_REMOTE='https://github.com/phronesis-io/eigenflux.git'
LOCK_FILE='/run/lock/eigenflux-deploy-main.lock'
STATE_DIR='/var/lib/eigenflux-deployer'
RUNTIME_ENV='/etc/eigenflux/runtime.env'
export PATH='/snap/go/current/bin:/snap/bin:/usr/sbin:/usr/bin:/sbin:/bin:/usr/local/bin'

if [[ "${EUID}" -ne 0 ]]; then
  echo "This command is managed by root. Use the systemd deployment service." >&2
  exit 1
fi

if [[ "${PROJECT_ROOT}" == *'@@'* || "${DEPLOY_LIB}" == *'@@'* || "${DEPLOY_USER}" == *'@@'* || "${DEPLOY_HOME}" == *'@@'* ]]; then
  echo "Run the installed root-managed copy, not this source template." >&2
  exit 1
fi

# shellcheck source=deploy_main_lib.sh
source "${DEPLOY_LIB}"
deploy_main_run "${PROJECT_ROOT}" "${LOCK_FILE}" "${DEPLOY_USER}" "${DEPLOY_HOME}" \
  "${OFFICIAL_REMOTE}" "${STATE_DIR}" "${RUNTIME_ENV}" "$@"
